# ably-publish-latency-debugger

A Go program which publishes to Ably using REST publish with debugging information.

The program outputs WARN logs when requests take longer than 100ms, which should be
shared with the Ably support team when reporting unexpected high latency.

## Quick Start

- [Install Go](https://go.dev/doc/install), at least version 1.24.0

- Build the binary:

```
$ go build .
```

- Set the `ABLY_API_KEY` environment variable to your Ably API key:

```
export ABLY_API_KEY='6mRzqQ.PNtHZA:xxxxxxxxxxxx'
```

- Run the program:

```
$ ./ably-publish-latency-debugger
2025/03/04 00:05:35 INFO checking ABLY_API_KEY is set
2025/03/04 00:05:35 INFO set publish interval interval=1s
2025/03/04 00:05:35 INFO starting metrics server port=3000
2025/03/04 00:05:35 INFO starting time requests baseURL=https://global.a.fallback.main.cluster.ably-realtime.com
2025/03/04 00:05:35 INFO starting fixed channel publisher channel=ably-publish-latency-debugger-1 baseURL=https://rest.ably.io
2025/03/04 00:05:35 INFO starting time requests baseURL=https://rest.ably.io
2025/03/04 00:05:35 INFO starting fixed channel publisher channel=ably-publish-latency-debugger-1 baseURL=https://global.a.fallback.main.cluster.ably-realtime.com
```

- Fetch the program's metrics:

```
$ curl http://127.0.0.1:3000/metrics
# HELP go_gc_duration_seconds A summary of the wall-time pause (stop-the-world) duration in garbage collection cycles.
# TYPE go_gc_duration_seconds summary
go_gc_duration_seconds{quantile="0"} 0.000114876
go_gc_duration_seconds{quantile="0.25"} 0.000114876
go_gc_duration_seconds{quantile="0.5"} 0.000114876
go_gc_duration_seconds{quantile="0.75"} 0.000114876
go_gc_duration_seconds{quantile="1"} 0.000114876
go_gc_duration_seconds_sum 0.000114876
go_gc_duration_seconds_count 1
...
```

## High Latency Logs

When a request takes longer than 100ms, the program outputs a WARN log like the following:

```
2025/03/04 00:22:14 WARN received publish response with high latency channel=ably-publish-latency-debugger-1 id=1135dbd70a2ba052 duration=496.968ms before=447.43ms after=49.541ms server=frontdoor.90c2.ap-southeast-1-A.i-0f8ebb6a79e2471fd.a2dXZAEpwnyegc cfid="" body="{\n\t\"channel\": \"ably-publish-latency-debugger-1\",\n\t\"messageId\": \"Azz6Su9MnB:0\"\n}"
```

When working with the Ably support team to investigate high latency, please provide these warning logs.

## Metrics

The program starts a metrics server on port 3000 (or the port specified in the `METRICS_PORT`
environment variable) which returns metrics in Prometheus format.

Configure Prometheus to scrape thse metrics with the following scrape config in your `prometheus.yml`:

```
- job_name: "ably-publish-latency-debugger"
  static_configs:
    - targets: ["localhost:3000"]
```

The program outputs two latency related metrics:

- `publish_latency_seconds` - a histogram which tracks the time spent waiting for publish requests to return a response
- `time_latency_seconds` - a histogram which tracks the time spent waiting for time requests to return a response

Both metrics have two labels:

- `baseURL` - the Ably URL the request was made to, either a primary or fallback URL
- `trace` - the section of the request the metric represents, one of:
    - `total` - the total time spent
    - `before` - the time between sending the request and Ably's reported handle time
    - `before` - the time between Ably's reported handle time and receiving the response
