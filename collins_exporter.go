package main

import (
	"flag"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
	"gopkg.in/tumblr/go-collins.v0/collins"
)

const namespace = "collins"

// statusNames lists the possible Collins status strings for an asset.
var statusNames = []string{
	"Incomplete",     // Host not yet ready for use. It has been powered on and entered in Collins but burn-in is likely being run.
	"New",            // Host has completed the burn-in process and is waiting for an onsite tech to complete physical intake.
	"Unallocated",    // Host has completed intake process and is ready for use.
	"Provisioning",   // Host has started provisioning process but has not yet completed it.
	"Provisioned",    // Host has finished provisioning and is awaiting final automated verification.
	"Allocated",      // This asset is in what should likely be considered a production state.
	"Cancelled",      // Asset is no longer needed and is awaiting decommissioning.
	"Decommissioned", // Asset has completed the outtake process and can no longer be managed.
	"Maintenance",    // Asset is undergoing some kind of maintenance and should not be considered for production use.
}

// Exporter collects Collins stats from the given endpoint and exports them
// via the prometheus.Collector interface.
type Exporter struct {
	client *collins.Client

	lastScrapeResult []prometheus.Metric
	requestScrape    chan struct{}
	scrapeResult     chan []prometheus.Metric

	up, scrapeDuration           prometheus.Gauge
	scrapesTotal, scrapeFailures prometheus.Counter

	assetStatusDesc, assetStateDesc, assetDetailsDesc *prometheus.Desc
}

func newCollinsClient(collinsConfig string) (*collins.Client, error) {
	if collinsConfig != "" {
		return collins.NewClientFromFiles(collinsConfig)
	}
	return collins.NewClientFromYaml()
}

// NewExporter returns an initialized Exporter.
func NewExporter(collinsConfig string) *Exporter {

	client, err := newCollinsClient(collinsConfig)
	if err != nil {
		log.Errorf("Could not set up collins client: %s", err)
	}

	return &Exporter{
		client:        client,
		requestScrape: make(chan struct{}),
		scrapeResult:  make(chan []prometheus.Metric),

		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "'1' if the last scrape of Collins was successful, '0' otherwise.",
		}),
		scrapeDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "scrape_duration_seconds",
			Help:      "The duration it took to scrape Collins.",
		}),
		scrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrapes_total",
			Help:      "Total number of Collins scrapes.",
		}),
		scrapeFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_failures_total",
			Help:      "Total number of failures scraping Collins.",
		}),
		assetStatusDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "asset", "status"),
			"'1' if the asset with the given tag has the given Collins status, '0' otherwise.",
			[]string{"tag", "status"},
			nil,
		),
		assetStateDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "asset", "state"),
			"The numerical Collins state ID for the asset with the given tag.",
			[]string{"tag"},
			nil,
		),
		assetDetailsDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "asset", "details"),
			"Constant metric with value '1' providing details for the asset with the given tag as labels.",
			[]string{"tag", "nodeclass", "ipmi_address", "primary_address"},
			nil,
		),
	}
}

// Loop manages scrapes of Collins triggered by scrapes of the exporter.
func (e *Exporter) Loop() {
	for {
		select {
		case <-e.requestScrape:
			e.scrapeCollins()
		case e.scrapeResult <- e.lastScrapeResult:
		}
	}
}

func (e *Exporter) scrapeCollins() {
	log.Debugln("Starting Collins scrape...")
	e.lastScrapeResult = nil

	start := time.Now()
	assets, err := getAllAssets(e.client)
	took := time.Since(start)
	e.scrapeDuration.Set(took.Seconds())
	e.scrapesTotal.Inc()
	log.Infof("Collins scrape finished, found %d assets in %v", len(assets), took)

	if err != nil {
		e.up.Set(0)
		e.scrapeFailures.Inc()
		// While there might be asset data retrieved, we do not want to
		// create metrics based on partial results. Thus, return here.
		// However, should we ever wish to return metrics based on
		// partial results, this would be the place to change.
		return
	}
	e.up.Set(1)

	for _, asset := range assets {
		primaryAddress := ""
		if len(asset.Addresses) > 0 {
			primaryAddress = asset.Addresses[0].Address
		}

		for _, status := range statusNames {
			var value float64
			if asset.Metadata.Status == status {
				value = 1
			}
			e.lastScrapeResult = append(e.lastScrapeResult, prometheus.MustNewConstMetric(
				e.assetStatusDesc,
				prometheus.GaugeValue,
				value,
				asset.Metadata.Tag, status,
			))
		}
		e.lastScrapeResult = append(e.lastScrapeResult, prometheus.MustNewConstMetric(
			e.assetStateDesc,
			prometheus.GaugeValue,
			float64(asset.Metadata.State.ID),
			asset.Metadata.Tag,
		))
		e.lastScrapeResult = append(e.lastScrapeResult, prometheus.MustNewConstMetric(
			e.assetDetailsDesc,
			prometheus.GaugeValue,
			1,
			asset.Metadata.Tag, asset.Classification.Tag, asset.IPMI.Address, primaryAddress,
		))
	}
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.assetStatusDesc
	ch <- e.assetStateDesc
	ch <- e.up.Desc()
	ch <- e.scrapesTotal.Desc()
	ch <- e.scrapeFailures.Desc()
	ch <- e.scrapeDuration.Desc()
}

// Collect implements prometheus.Collector. It only initiates a scrape of
// Collins if no scrape is currently ongoing. If a scrape of Collins is
// currently ongoing, Collect waits for it to end and then uses its result to
// collect the metrics.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	select {
	case e.requestScrape <- struct{}{}:
	default: // Scraping already underway.
	}
	for _, metric := range <-e.scrapeResult {
		ch <- metric
	}
	ch <- e.up
	ch <- e.scrapesTotal
	ch <- e.scrapeFailures
	ch <- e.scrapeDuration
}

// getAllAssets retrieves the asset data from collins and returns it. It returns
// any encountered error. Even if the returned error is not nil, there might be
// assets in the returned slice if the error was only encountered midway during
// the reterieval.
func getAllAssets(client *collins.Client) ([]collins.Asset, error) {

	opts := collins.AssetFindOpts{
		Query:    "TYPE = SERVER_NODE AND NOT STATUS = incomplete",
		PageOpts: collins.PageOpts{Page: 0, Size: 1000},
	}

	assets, resp, err := client.Assets.Find(&opts)
	if err != nil {
		log.Errorf("Assets.Find returned error: %s", err)
		return nil, err
	}
	log.Debugf("Found %d assets, %d total", len(assets), resp.TotalResults)

	allAssets := make([]collins.Asset, 0, resp.TotalResults)
	allAssets = append(allAssets, assets...)

	for opts.PageOpts.Page++; resp.NextPage > resp.CurrentPage; opts.PageOpts.Page++ {
		assets, resp, err = client.Assets.Find(&opts)
		if err != nil {
			log.Errorf("Assets.Find returned error: %s", err)
			break
		}
		log.Debugf("Found %d more assets", len(assets))

		allAssets = append(allAssets, assets...)
	}

	return allAssets, err
}

func main() {
	var (
		listenAddress = flag.String("web.listen-address", ":9136", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		collinsConfig = flag.String("collins.config", "", "Path to Collins config (https://tumblr.github.io/collins/tools.html#configs). Defaults to common locations.")
	)
	flag.Parse()

	log.Infoln("Starting collins_exporter")

	exporter := NewExporter(*collinsConfig)
	go exporter.Loop()
	prometheus.MustRegister(exporter)

	log.Infoln("Listening on", *listenAddress)
	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Collins Exporter</title></head>
             <body>
             <h1>Collins Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})
	err := http.ListenAndServe(*listenAddress, nil)
	if err != nil {
		log.Fatal(err)
	}
}
