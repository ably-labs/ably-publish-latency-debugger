package main

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	ablyBaseURL   = cmp.Or(os.Getenv("ABLY_BASE_URL"), "https://rest.ably.io")
	apiKey        = os.Getenv("ABLY_API_KEY")
	channelPrefix = os.Getenv("ABLY_CHANNEL_PREFIX")
	endpoint      = os.Getenv("ABLY_ENDPOINT")
	intervalEnv   = cmp.Or(os.Getenv("PUBLISH_INTERVAL"), "1s")
	verbose       = os.Getenv("VERBOSE") != ""
	metricsPort   = cmp.Or(os.Getenv("METRICS_PORT"), "3000")

	apiKeyBase64 string
	interval     time.Duration
)

const (
	highLatencyThreshold = 100 * time.Millisecond
	publishBody          = `{"name":"ably-publish-latency-debugger","data":"this is a test from ably-publish-latency-debugger"}`
)

func main() {
	// stop on SIGINT or SIGTERM
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// enable debug logs if VERBOSE is set
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	// run until stopped
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("error running ably-publish-latency-debugger", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	slog.Info("checking ABLY_API_KEY is set")
	if apiKey == "" {
		slog.Error("missing ABLY_API_KEY environment variable")
		return errors.New("missing ABLY_API_KEY environment variable")
	}

	var err error
	interval, err = time.ParseDuration(intervalEnv)
	if err != nil {
		slog.Error("error parsing PUBLISH_INTERVAL environment variable", "error", err)
		return err
	}
	slog.Info("set publish interval", "interval", interval)

	// cache the base64 encoding of the API key to be used in the basic
	// auth header
	apiKeyBase64 = base64.StdEncoding.EncodeToString([]byte(apiKey))

	// make sure ablyBaseURL doesn't have a slash at the end
	if strings.HasSuffix(ablyBaseURL, "/") {
		ablyBaseURL = strings.TrimSuffix(ablyBaseURL, "/")
	}

	slog.Info("starting metrics server", "port", metricsPort)
	ln, err := net.Listen("tcp", ":"+metricsPort)
	if err != nil {
		slog.Error("error starting metrics server", "error", err)
		return err
	}
	defer ln.Close()
	go func() { http.Serve(ln, promhttp.Handler()) }()

	var wg sync.WaitGroup

	// start a goroutine which publishes to a fixed channel
	fixedChannelName := nextChannelName()
	wg.Add(1)
	go func() {
		defer wg.Done()
		log := slog.With("channel", fixedChannelName)
		log.Info("starting fixed channel publisher")

		timer := time.NewTimer(interval)
		for {
			select {
			case <-timer.C:
				// run the publish in a goroutine to avoid
				// blocking the timer
				go func() {
					log.Debug("publishing to fixed channel")
					if err := publish(ctx, fixedChannelName, log); err != nil {
						slog.Error("error publishing to fixed channel", "error", err)
					}
				}()
				timer.Reset(interval)
			case <-ctx.Done():
				log.Info("stopping fixed channel publisher")
				return
			}
		}
	}()

	// TODO: start random channel publisher
	// TODO: start request token publisher

	wg.Wait()
	slog.Info("exiting")
	return nil
}

// channelNumber is used to assign an incremeting number to each channel.
var channelNumber atomic.Int32

// nextChannelName returns the next channel name to use, which is prefixed
// by ABLY_CHANNEL_PREFIX, and suffixed with the next channelNumber.
func nextChannelName() string {
	return fmt.Sprintf("%sably-publish-latency-debugger-%d", channelPrefix, channelNumber.Add(1))
}

// publish publishes a message to Ably over REST with debugging information
// in the URL.
func publish(ctx context.Context, channelName string, log *slog.Logger) error {
	id := fmt.Sprintf("%016x", rand.Int64())
	log = log.With("id", id)

	start := time.Now().UTC()
	url := ablyBaseURL + "/channels/" + channelName + "/messages?ably-publish-latency-debugger=id:" + id + ",start:" + strconv.FormatInt(start.UnixMicro(), 10)
	log.Debug("publishing message", "url", url)

	ctx = httptrace.WithClientTrace(ctx, newClientTrace(start, log))

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(publishBody))
	if err != nil {
		log.Error("error initialising HTTP request", "error", err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+apiKeyBase64)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Error("error sending HTTP request", "error", err)
		return err
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		log.Error("error publishing message", "status", res.StatusCode, "body", body)
		return err
	}
	duration := time.Since(start)
	server := res.Header.Get("X-Ably-Serverid")
	cfid := res.Header.Get("X-Amz-Cf-Id")
	log.Debug("received publish response", "duration", duration, "server", server, "cfid", cfid, "body", body)

	publishLatencySeconds.Observe(duration.Seconds())
	if duration > highLatencyThreshold {
		log.Warn("received publish response with high latency", "duration", duration, "server", server, "cfid", cfid, "body", body)
	}
	return nil
}

func newClientTrace(start time.Time, log *slog.Logger) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			log.Debug("got connection", "duration", time.Since(start), "addr", info.Conn.RemoteAddr(), "reused", info.Reused)
		},
		WroteHeaders: func() {
			log.Debug("wrote headers", "duration", time.Since(start))
		},
		WroteRequest: func(_ httptrace.WroteRequestInfo) {
			log.Debug("wrote request", "duration", time.Since(start))
		},
		GotFirstResponseByte: func() {
			log.Debug("got first response byte", "duration", time.Since(start))
		},
	}
}

// latencyBuckets are exponential histogram buckets which are logarithmically
// spaced to cover both small and large latencies.
//
// They use a factor of 16th root of 10 so that each 16th successive boundary
// is a power of 10 (i.e. 1ms, 10ms, 100ms, 1s, 10s), which is useful for
// accurate counts of latencies below those human friendly boundaries.
//
// Here's the list of bucket boundaries this generates:
//
// 1.000ms  10.00ms  100.0ms  1.000s  10.00s
// 1.155ms  11.55ms  115.5ms  1.155s
// 1.334ms  13.34ms  133.4ms  1.334s
// 1.540ms  15.40ms  154.0ms  1.540s
// 1.778ms  17.78ms  177.8ms  1.778s
// 2.054ms  20.54ms  205.4ms  2.054s
// 2.371ms  23.71ms  237.1ms  2.371s
// 2.738ms  27.38ms  273.8ms  2.738s
// 3.162ms  31.62ms  316.2ms  3.162s
// 3.652ms  36.52ms  365.2ms  3.652s
// 4.217ms  42.17ms  421.7ms  4.217s
// 4.870ms  48.70ms  487.0ms  4.870s
// 5.623ms  56.23ms  562.3ms  5.623s
// 6.494ms  64.94ms  649.4ms  6.494s
// 7.499ms  74.99ms  749.9ms  7.499s
// 8.660ms  86.60ms  866.0ms  8.660s
var latencyBuckets = prometheus.ExponentialBuckets(
	0.001,
	math.Pow(10, float64(1)/16),
	65,
)

var (
	publishLatencySeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "publish_latency_seconds",
			Help:    "Time spent waiting for publish requests to return a response",
			Buckets: latencyBuckets,
		},
	)
)
