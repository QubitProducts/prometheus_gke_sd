package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	log "github.com/golang/glog"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/context"
	"golang.org/x/net/context/ctxhttp"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v2"

	google "golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	container "google.golang.org/api/container/v1"
)

var (
	configInputFile  = "/etc/gke-input.yml"
	configOutputFile = "/etc/gke-output.yml"

	prometheusAddress = "http://prometheus:9090"

	certOutDir       = "/etc/gke-certs"
	certReferenceDir = "/etc/gke-certs"

	gcpProject   = ""
	pollInterval = time.Second * 10

	clusterCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gkesd_clusters",
		Help: "Number of clusters discovered",
	})
	syncDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "gkesd_sync_duration_seconds",
		Help: "Duration of the GKE api to prometheus config sync operation",
	})
	syncResult = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gkesd_sync_count",
		Help: "Count of the GKE api to prometheus config sync operation, labeled by result",
	}, []string{"result"})
)

const (
	debounceDuration = time.Second * 5

	reloadInterval = time.Second
	reloadBackoff  = 1.1
)

func init() {
	flag.StringVar(&configInputFile, "prometheus.config-input", configInputFile, "Prometheus config file to augment with GKE clusters")
	flag.StringVar(&configOutputFile, "prometheus.config-output", configOutputFile, "Location to write augmented prometheus config file")

	flag.StringVar(&prometheusAddress, "prometheus.address", prometheusAddress, "Address of Prometheus server to reload")

	flag.StringVar(&certOutDir, "prometheus.cert.output-path", certOutDir, "Directory to write GKE certificates to")
	flag.StringVar(&certReferenceDir, "prometheus.cert.reference-path", certReferenceDir, "Path in prometheus config to reference GKE certificates")

	flag.StringVar(&gcpProject, "gcp.project", "", "GCP project to discover clusters in")
	flag.DurationVar(&pollInterval, "poll-interval", pollInterval, "Interval to poll for new GKE clusters at")

	prometheus.MustRegister(clusterCount)
	prometheus.MustRegister(syncDuration)
	prometheus.MustRegister(syncResult)
}

type PrometheusConfig struct {
	ScrapeConfigs []ScrapeConfig         `yaml:"scrape_configs"`
	XXX           map[string]interface{} `yaml:",inline"`
}

type TLSConfig struct {
	CAFile   string `yaml:"ca_file,omitempty"`
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`
}
type BasicAuth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type KubeSDConfig struct {
	APIServers []string  `yaml:"api_servers"`
	Role       string    `yaml:"role"`
	InCluster  bool      `yaml:"in_cluster,omitempty"`
	TLSConfig  TLSConfig `yaml:"tls_config,omitempty"`
}

type ScrapeConfig struct {
	JobName             string          `yaml:"job_name"`
	KubernetesSDConfigs []KubeSDConfig  `yaml:"kubernetes_sd_configs,omitempty"`
	RelabelConfigs      []RelabelConfig `yaml:"relabel_configs,omitempty"`
	BasicAuth           `yaml:"basic_auth,omitempty"`
	XXX                 map[string]interface{} `yaml:",inline"`
}

func main() {
	flag.Parse()
	if gcpProject == "" {
		log.Error("Please supply a GCP Project")
		os.Exit(1)
	}

	ctx := context.Background()

	log.V(2).Infof("Checking config every %v or on changes to %v", pollInterval, configInputFile)
	updateChan, err := watchAndTick(ctx, configInputFile, pollInterval)
	if err != nil {
		log.Fatalf("Failed to watch input file: %v", err)
	}

	currentClusters := []*container.Cluster{}

	loop := func(force bool) error {
		started := time.Now()
		defer syncDuration.Observe(float64(time.Now().Sub(started)) / float64(time.Second))

		ctx, cancel := context.WithTimeout(ctx, pollInterval)
		defer cancel()

		newClusters, err := findClusters(ctx, gcpProject)
		if err != nil {
			return errors.Wrap(err, "could not find clusters")
		}

		if !force {
			changes := !clusterListEqual(currentClusters, newClusters)
			if !changes {
				return nil
			}
			log.V(2).Infof("Change in clusters composition")
		} else {
			log.V(2).Infof("Forcing reload")
		}

		if log.V(2) {
			log.Infof("Clusters:")
			for _, c := range newClusters {
				log.Info(c.Name)
			}
		}
		clusterCount.Set(float64(len(newClusters)))

		err = writeClusterCerts(certOutDir, newClusters)
		if err != nil {
			return errors.Wrap(err, "could not update cluster certs")
		}
		log.V(2).Infof("Wrote certs to %v", certOutDir)

		newConfig, err := generateConfig(configInputFile, certReferenceDir, newClusters)
		if err != nil {
			return errors.Wrap(err, "could not generate config")
		}
		err = ioutil.WriteFile(configOutputFile, newConfig, 0600)
		if err != nil {
			return errors.Wrap(err, "could not write config")
		}
		log.V(2).Infof("Wrote config to %v", configOutputFile)

		err = reloadPrometheus(ctx, prometheusAddress)
		if err != nil {
			return errors.Wrap(err, "could not reload prometheus")
		}

		// Only set new clusters after a successful reload
		currentClusters = newClusters
		return nil
	}

	for force := range updateChan {
		err := loop(force)
		if err != nil {
			log.Errorf("Config check/update loop failed: %v", err)
			syncResult.WithLabelValues("failure").Inc()
		} else {
			syncResult.WithLabelValues("success").Inc()
		}
	}
}

func reloadPrometheus(ctx context.Context, prometheusLocation string) error {
	url := fmt.Sprintf("%v/-/reload", prometheusLocation)
	backoff := reloadInterval
	for i := 0; ctx.Err() == nil; i++ {
		log.V(2).Infof("Reloading prometheus")
		_, err := ctxhttp.Post(ctx, http.DefaultClient, url, "", nil)
		if err == nil {
			log.Infof("Reloaded prometheus")
			return nil
		}
		log.Errorf("Failed to reload prometheus: %v", err)

		log.V(2).Infof("Backing off for %v", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
		}
		backoff = time.Duration(float64(backoff) * reloadBackoff)
	}
	return ctx.Err()
}

func writeClusterCerts(outDir string, clusters []*container.Cluster) error {
	for _, cluster := range clusters {
		err := writeCert(outDir, cluster.Name, "ca", cluster.MasterAuth.ClusterCaCertificate)
		if err != nil {
			return errors.Wrap(err, "could not write ca cert")
		}
		err = writeCert(outDir, cluster.Name, "cert", cluster.MasterAuth.ClientCertificate)
		if err != nil {
			return errors.Wrap(err, "could not write client cert")
		}
		err = writeCert(outDir, cluster.Name, "key", cluster.MasterAuth.ClientKey)
		if err != nil {
			return errors.Wrap(err, "could not write client key")
		}
	}
	return nil
}

func writeCert(outDir, clusterName, certType, b64Cert string) error {
	cert, err := base64.StdEncoding.DecodeString(b64Cert)
	if err != nil {
		return errors.Wrap(err, "could not b64 decode cert")
	}
	fname := fmt.Sprintf("%v/%v-%v.pem", outDir, clusterName, certType)
	err = ioutil.WriteFile(fname, cert, 0600)
	return errors.Wrap(err, "could not write file")
}

func generateConfig(inputConfigFilename, certDir string, clusters []*container.Cluster) ([]byte, error) {
	inputConfig, err := readInputConfig(inputConfigFilename)
	if err != nil {
		return []byte{}, errors.Wrapf(err, "could not load input config at %v", inputConfigFilename)
	}

	scrapeConfigs := []ScrapeConfig{}
	for _, c := range clusters {
		scrapeConfigs = append(scrapeConfigs, clusterToScrapeConfigs(certDir, c)...)
	}

	inputConfig.ScrapeConfigs = append(inputConfig.ScrapeConfigs, scrapeConfigs...)

	data, err := yaml.Marshal(inputConfig)
	return data, errors.Wrap(err, "could not marshal config")
}

func clusterToScrapeConfigs(certDir string, cluster *container.Cluster) []ScrapeConfig {
	configs := []ScrapeConfig{}
	if cluster.Endpoint == "" {
		log.Errorf("No master endpoint defined for %v", cluster.Name)
		return configs
	}
	if log.V(3) {
		log.Infof("Cluster: %v Endpoint: %v", cluster.Name, "https://"+cluster.Endpoint)
		cd, err := json.Marshal(cluster)
		if err == nil {
			log.Infof("Cluster json: %v", string(cd))
		}
	}

	for r, c := range GetRoles() {
		configs = append(configs, ScrapeConfig{
			JobName: fmt.Sprintf("kubernetes_%v_%v", cluster.Name, r),
			BasicAuth: BasicAuth{
				Username: cluster.MasterAuth.Username,
				Password: cluster.MasterAuth.Password,
			},
			KubernetesSDConfigs: []KubeSDConfig{
				{
					APIServers: []string{
						"https://" + cluster.Endpoint,
					},
					Role:      r,
					InCluster: false,
					TLSConfig: TLSConfig{
						CAFile:   fmt.Sprintf("%v/%v-ca.pem", certDir, cluster.Name),
						CertFile: fmt.Sprintf("%v/%v-cert.pem", certDir, cluster.Name),
						KeyFile:  fmt.Sprintf("%v/%v-key.pem", certDir, cluster.Name),
					},
				},
			},
			RelabelConfigs: c,
		})
	}
	return configs
}

func readInputConfig(inputConfigFilename string) (PrometheusConfig, error) {
	data, err := ioutil.ReadFile(inputConfigFilename)
	if err != nil {
		return PrometheusConfig{}, errors.Wrap(err, "could not read input config")
	}

	config := PrometheusConfig{}
	err = yaml.Unmarshal(data, &config)
	return config, errors.Wrap(err, "could not parse input config")
}

// Returns a channel that will is a union of time.Tick and watchFile. Messages will be `true` if
// triggered by watchFile, otherwise `false`
func watchAndTick(ctx context.Context, fname string, interval time.Duration) (<-chan bool, error) {
	ch := make(chan bool)

	wch, err := watchFile(ctx, fname)
	if err != nil {
		return ch, err
	}
	tch := time.Tick(interval)

	go func() {
		ch <- false // Add an initial tick
		for {
			select {
			case <-wch:
				ch <- true
			case <-tch:
				ch <- false
			}
		}
	}()

	return ch, nil
}

func watchFile(ctx context.Context, fname string) (<-chan struct{}, error) {
	ch := make(chan struct{})

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return ch, errors.Wrap(err, "could not create fsnotify watcher")
	}

	err = watcher.Add(fname)
	if err != nil {
		return ch, errors.Wrapf(err, "could not watch %v", fname)
	}

	debounce := func() {
		log.V(4).Infof("Debouncing watch event for %v", debounceDuration)
		ctx, cancel := context.WithTimeout(ctx, debounceDuration)
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				log.V(4).Infof("Finished debounce")
				return
			case e := <-watcher.Events:
				log.V(4).Infof("Event debounced: %v", e)
			}
		}
	}

	go func() {
		for {
			select {
			case <-watcher.Events:
				debounce()
				ch <- struct{}{}
			case err := <-watcher.Errors:
				log.Errorf("Watcher failed: %v", err)
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

func clusterListEqual(old, new []*container.Cluster) bool {
	oldByName := map[string]bool{}
	newByName := map[string]bool{}

	for _, o := range old {
		oldByName[o.Name] = true
	}
	for _, n := range new {
		newByName[n.Name] = true
	}

	for _, o := range old {
		if _, ok := newByName[o.Name]; !ok {
			return false
		}
	}
	for _, n := range new {
		if _, ok := oldByName[n.Name]; !ok {
			return false
		}
	}

	return true
}

func findClusters(ctx context.Context, project string) ([]*container.Cluster, error) {
	client, err := google.DefaultClient(ctx, container.CloudPlatformScope, compute.ComputeReadonlyScope)
	if err != nil {
		return []*container.Cluster{}, errors.Wrap(err, "could not create google client")
	}

	zones, err := listZones(ctx, client, project)
	if err != nil {
		return []*container.Cluster{}, errors.Wrap(err, "could not list zones")
	}

	clusters := []*container.Cluster{}
	for _, z := range zones {
		zcs, err := listClusters(ctx, client, project, z)
		if err != nil {
			return []*container.Cluster{}, errors.Wrapf(err, "could not list clusters in %v/%v", project, z)
		}

		clusters = append(clusters, zcs...)
	}
	return clusters, nil
}

func listZones(ctx context.Context, client *http.Client, project string) ([]string, error) {
	svc, err := compute.New(client)
	if err != nil {
		return []string{}, errors.Wrap(err, "could not create compute service")
	}

	res, err := svc.Zones.List(project).Context(ctx).Do()
	if err != nil {
		return []string{}, errors.Wrap(err, "could not list zones")
	}

	zones := make([]string, 0, len(res.Items))
	for _, z := range res.Items {
		zones = append(zones, z.Name)
	}
	return zones, nil
}

func listClusters(ctx context.Context, client *http.Client, project, zone string) ([]*container.Cluster, error) {
	svc, err := container.New(client)
	if err != nil {
		return []*container.Cluster{}, errors.Wrap(err, "could not create container service")
	}

	res, err := svc.Projects.Zones.Clusters.List(project, zone).Context(ctx).Do()
	if err != nil {
		return []*container.Cluster{}, errors.Wrap(err, "could not list clusters")
	}

	return res.Clusters, nil
}
