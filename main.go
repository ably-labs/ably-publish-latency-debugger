package main

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
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
)

var (
	ablyBaseURL   = cmp.Or(os.Getenv("ABLY_BASE_URL"), "https://rest.ably.io")
	apiKey        = os.Getenv("ABLY_API_KEY")
	channelPrefix = os.Getenv("ABLY_CHANNEL_PREFIX")
	endpoint      = os.Getenv("ABLY_ENDPOINT")
	verbose       = os.Getenv("VERBOSE") != ""

	apiKeyBase64 string
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

	// cache the base64 encoding of the API key to be used in the basic
	// auth header
	apiKeyBase64 = base64.StdEncoding.EncodeToString([]byte(apiKey))

	// make sure ablyBaseURL doesn't have a slash at the end
	if strings.HasSuffix(ablyBaseURL, "/") {
		ablyBaseURL = strings.TrimSuffix(ablyBaseURL, "/")
	}

	var wg sync.WaitGroup

	// start a goroutine which publishes to a fixed channel
	fixedChannelName := nextChannelName()
	wg.Add(1)
	go func() {
		defer wg.Done()
		log := slog.With("channel", fixedChannelName)
		log.Info("starting fixed channel publisher")

		timer := time.NewTimer(time.Second)
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
				timer.Reset(time.Second)
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
