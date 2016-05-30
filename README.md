Prometheus Collins Exporter
===========================

This is a [Collins](https://github.com/tumblr/collins/) exporter for
[Prometheus](https://prometheus.io).

This exporter usually retrieves the necessary data from Collins (called a
“Collins scrape”) upon each scrape of the `/metrics` endpoint (the “Prometheus
scrape”). However, Collins can become slow at times, especially if multiple
requests are overlapping. Therefore, whenever a Prometheus scrape hits the
exporter while a Collins scrape is still ongoing, no new Collins scrape will be
started, but all pending Prometheus scrapes will get metrics from the current
Collins scrape once it has finished. (This adds jitter, but exporting from a
potentially slow backend has jitter anyway. Arguably, not applying this
protection against overloading Collins will make things even worse.)

Despite this precaution, a Collins scrape might still take longer than 10s for
large inventories. Take that into account when configuring the scrape timeout
on your Prometheus server.

## Installing

You need a Go development environment. Then, run the following to get the
source code and build and install the binary:

    go get github.com/soundcloud/collins_exporter`

## Running

A minimal invocation is simply:

    ./collins_exporter

Supported parameters include:

 - `web.listen-address`: the address/port to listen on (default: `":9136"`)
 - `web.telemetry-path`: the path under which to expose metrics (default:
   `"/metrics"`)
 - `collins.config`: the path to your Collins config, if not in a standard
   location (see https://tumblr.github.io/collins/tools.html#configs)

## Digging into the data

The exporter exposes three major groups of metrics, `collins_asset_status`,
`collins_asset_state`, and `collins_asset_details`, with the Collins asset tag
being used as a label for each one.

### Asset info

The `collins_asset_details` metrics always have a value of one. But they
contain useful information about each asset in their labels. A typical metric
looks like this:

```
collins_asset_details{instance="collins.example.com:9139",ipmi_address="10.1.2.3",job="collins",nodeclass="web-server",primary_address="10.10.20.30",tag="ABCD1234"}
```

The information encoded in this series can be used to find assets by attributes
other then the asset tag, as demonstrated in the example queries below.

### Status

There is one `collins_asset_status` metric per asset tag and per possible
Collins status. Since there are (currently) nine different Collins statuses,
the query `collins_asset_status{tag="ABCD1234"}` will yield nine metrics, each
with a different `status` label like `"New"` or `"Allocated"`. The value of
each but one of the metrics will be 0. The one metric with a value of 1
represents the status the asset is currently in.

A useful query to get started is to list the number of assets per status per
nodeclass:

```
count(((collins_asset_status == 1) * on(tag) group_right(status) collins_asset_details)) by (job, nodeclass, status)
```

In fact, it's so useful you might want to use a recording rule for it:

```
status_nodeclass:collins_asset_status:count = count(((collins_asset_status == 1) * on(tag) group_right(status) collins_asset_details)) by (job, nodeclass, status)
```

Based on that, you could for example display the percentage of unallocated
(available) assets per nodeclass on a dashboard:

```
sum(status_nodeclass:collins_asset_status:count{status="Unallocated"}) without (status) / sum(status_nodeclass:collins_asset_status:count) without (status)
```

Sometimes you need to get the status of a machine but don't know the asset tag
yet. Here is a sample query for the asset status using only the primary IP
address. It returns the asset tag, the status name, and the nodeclass as
labels:

```
collins_asset_details{primary_address="10.1.2.3"} * on(tag) group_right(nodeclass) collins_asset_status == 1
```


### State

Unlike the fixed number of statuses, there can be an arbitrary number of
user-defined Collins states. Thus, the `collins_asset_state` metrics follow a
different approach. There is exactly one metric per asset, and its value
reflects the ID of the state. We are still looking for a good way of exposing
the state by name, too.
