package main

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"golang.org/x/net/context"
	"gopkg.in/yaml.v2"

	google "golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	container "google.golang.org/api/container/v1"
	kubeapi "k8s.io/kubernetes/pkg/api"
	restclient "k8s.io/kubernetes/pkg/client/restclient"
	kubeclient "k8s.io/kubernetes/pkg/client/unversioned"
	labels "k8s.io/kubernetes/pkg/labels"
	kubeselection "k8s.io/kubernetes/pkg/selection"
	kubesets "k8s.io/kubernetes/pkg/util/sets"
)

var (
	configFile = "/etc/gke-discoverer.yml"
)

func init() {
	flag.StringVar(&configFile, "config", configFile, "config file to use")
}

type Config struct {
	WritePrometheusConfigMap string `yaml:"write_prometheus_config_map"`
	ReadPrometheusConfigMap  string `yaml:"read_prometheus_config_map"`
	CertificateStoreDir			 string `yaml:"certificate_store_dir"`
	CertificateConfigMap		 string `yaml:"certificate_config_map"`
	PrometheusPodLabel       string `yaml:"prometheus_label"`
	GCPProject               string `yaml:"gcp_project"`
	PollTime                 int64  `yaml:"poll_time"`
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

func LoadConfig(filename string) Config {
	cfg := Config{}
	d, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatal(err)
	}

	err = yaml.Unmarshal(d, &cfg)
	if err != nil {
		log.Fatal(err)
	}

	// Defaults
	if cfg.ReadPrometheusConfigMap == "" {
		cfg.ReadPrometheusConfigMap = "prometheus"
	}
  if cfg.WritePrometheusConfigMap == "" {
		cfg.WritePrometheusConfigMap = "prometheus-dynamic"
	}

	if cfg.PrometheusPodLabel == "" {
		cfg.PrometheusPodLabel = "prometheus"
	}

	if cfg.PollTime == 0 {
		cfg.PollTime = 30
	}

	if cfg.GCPProject == "" {
		log.Fatal("Please supply a GCP Project")
	}

	return cfg
}

func main() {
	flag.Parse()
	cfg := LoadConfig(configFile)

	// Create google gubbins.
	client, err := google.DefaultClient(context.TODO(), container.CloudPlatformScope, compute.ComputeReadonlyScope)
	if err != nil {
		log.Fatal(err)
	}

	containerSvc, err := container.New(client)
	if err != nil {
		log.Fatal(err)
	}

	computeSvc, err := compute.New(client)
	if err != nil {
		log.Fatal(err)
	}

	oldClusters := []container.Cluster{}

	// Kubernetes Gubbins
	confDir := "/var/run/secrets/kubernetes.io/serviceaccount"
	caData, err := ioutil.ReadFile(confDir + "/ca.crt")
	if err != nil {
		log.Fatal(err)
	}

	token, err := ioutil.ReadFile(confDir + "/token")
	if err != nil {
		log.Fatal(err)
	}

	namespace, err := ioutil.ReadFile(confDir + "/namespace")
	if err != nil {
		log.Fatal(err)
	}

	// Kube Client
	config := &restclient.Config{
		Host:            "https://kubernetes",
		BearerToken:     string(token),
		TLSClientConfig: restclient.TLSClientConfig{CAData: caData},
	}

	c, err := kubeclient.New(config)
	if err != nil {
		fmt.Println(err)
	}
	hasChanged := false

	ticker := time.NewTicker(time.Duration(cfg.PollTime) * time.Second)

	for {
		select {
		case <-ticker.C:
			hasChanged = false
			res, err := computeSvc.Zones.List(cfg.GCPProject).Do()
			if err != nil {
				log.Fatal(err)
			}

			// Check every zone.
			newClusterList := []container.Cluster{}
			for _, z := range res.Items {

				fmt.Println("Zone : ", z.Name)
				res, err := containerSvc.Projects.Zones.Clusters.List(cfg.GCPProject, z.Name).Do()
				if err != nil {
					log.Fatal(err)
				}
				for _, c := range res.Clusters {
					newClusterList = append(newClusterList, *c)
				}
			}

			if len(oldClusters) == 0 {
				oldClusters = newClusterList
				hasChanged = true
			}

			indexesToRemove := []int{}

			// What entries are in the new cluster, but not in the old? (I.e New Entries)
			for _, cluster := range newClusterList {
				hasFound := false
				for _, ocluster := range oldClusters {
					if cluster.Name == ocluster.Name {
						hasFound = true
					}
				}
				if !hasFound {
					oldClusters = append(oldClusters, cluster)
					hasChanged = true
				}
			}

			// What needs to be cleaned up (i.e Clusters that have been deleted)
			for i, ocluster := range oldClusters {
				hasFound := false
				for _, cluster := range newClusterList {
					if cluster.Name == ocluster.Name {
						hasFound = true
					}
				}

				if !hasFound {
					indexesToRemove = append(indexesToRemove, i)
					hasChanged = true
				}
			}
			// Remove old entries
			for _, i := range indexesToRemove {
				oldClusters = oldClusters[:i+copy(oldClusters[i:], oldClusters[i+1:])]
			}

			if !hasChanged {
				fmt.Println("No Difference in config")
				break
			}
			fmt.Println("Detected Changed in Config:", len(oldClusters), len(newClusterList))
			fmt.Println("Old Clusters:")
			for _, c := range oldClusters {
				fmt.Println(c.Name)
			}
			fmt.Println("New Clusters:")
			for _, c := range newClusterList {
				fmt.Println(c.Name)
			}

			newScrapeConfigs := []ScrapeConfig{}

			cfgMapCerts := &kubeapi.ConfigMap{
				ObjectMeta: kubeapi.ObjectMeta{
					Name: cfg.CertificateConfigMap,
				},
				Data: map[string]string{},
			}


			for _, cluster := range oldClusters {
				CAFile := fmt.Sprintf("%v-ca.pem", cluster.Name)
				CertFile := fmt.Sprintf("%v-cert.pem", cluster.Name)
				KeyFile := fmt.Sprintf("%v-key.pem", cluster.Name)

				decodedCA, err := base64.StdEncoding.DecodeString(cluster.MasterAuth.ClusterCaCertificate)
				if err != nil {
					log.Fatal(err)
				}
				decodedCert, err := base64.StdEncoding.DecodeString(cluster.MasterAuth.ClientCertificate)
				if err != nil {
					log.Fatal(err)
				}
				decodedKey, err := base64.StdEncoding.DecodeString(cluster.MasterAuth.ClientKey)
				if err != nil {
					log.Fatal(err)
				}

				cfgMapCerts.Data[CAFile] = string(decodedCA)
				cfgMapCerts.Data[CertFile] = string(decodedCert)
				cfgMapCerts.Data[KeyFile] = string(decodedKey)

				for r, c := range GetRoles() {
					scc := ScrapeConfig{
						JobName: fmt.Sprintf("kubernetes_%v_%v", cluster.Name, r),
						BasicAuth: BasicAuth{
							Username: cluster.MasterAuth.Username,
							Password: cluster.MasterAuth.Password,
						},
						KubernetesSDConfigs: []KubeSDConfig{
							KubeSDConfig{
								APIServers: []string{
									"https://" + cluster.Endpoint,
								},
								Role:      r,
								InCluster: false,
								/*
									This has to be the path where prometheus will expect to find the certs
									At the moment, this is the only reason we have to pass in the CertificateStoreDir variable
									Potentially, this could come from the prometheus deployment in kube (by looking up where we're mounting the certs)
								*/
								TLSConfig: TLSConfig{
									CAFile:   fmt.Sprintf("%v/%v", cfg.CertificateStoreDir, CAFile),
									CertFile: fmt.Sprintf("%v/%v", cfg.CertificateStoreDir, CertFile),
									KeyFile:  fmt.Sprintf("%v/%v", cfg.CertificateStoreDir, KeyFile),
								},
							},
						},
						RelabelConfigs: c,
					}

					newScrapeConfigs = append(newScrapeConfigs, scc)
				}
			}

			cfgp := PrometheusConfig{}
			cfgMap, err := c.ConfigMaps(string(namespace)).Get(cfg.ReadPrometheusConfigMap)
			if err != nil {
				fmt.Println(err)
			}

			err = yaml.Unmarshal([]byte(cfgMap.Data["prometheus.yml"]), &cfgp)
			if err != nil {
				log.Fatal(err)
			}

			for _, sc := range cfgp.ScrapeConfigs {
				// I.e we're not a kube scrape config, and we were in the original file - lets leave this alone
				if len(sc.KubernetesSDConfigs) == 0 {
					newScrapeConfigs = append(newScrapeConfigs, sc)
				}
			}

			cfgp.ScrapeConfigs = newScrapeConfigs

			d, err := yaml.Marshal(&cfgp)
			if err != nil {
				log.Fatal(err)
			}

			// Prometheus
			cfgMap.Data["prometheus.yml"] = string(d)
			cfgMap.ObjectMeta.Name = cfg.WritePrometheusConfigMap
      cfgMap.ResourceVersion = ""
			cfgMap.SelfLink = ""

			// Certs
			fmt.Println("Creating Cert map", cfgMapCerts)
//			time.Sleep(time.Second * 30) // Kubernetes Update Sync period
			_, err = c.ConfigMaps(string(namespace)).Create(cfgMapCerts)
      if err != nil {
				fmt.Println(err)
				_, erri := c.ConfigMaps(string(namespace)).Update(cfgMapCerts)
				if erri != nil {
					fmt.Println(err)
					log.Fatal(erri)
				}
      }

			_, err = c.ConfigMaps(string(namespace)).Create(cfgMap)
			if err != nil {
				// Try to create it.
				fmt.Println(err)
				_, erri := c.ConfigMaps(string(namespace)).Update(cfgMap)
				if erri != nil {
					fmt.Println(err)
					log.Fatal(erri)
				}
			}

			fmt.Println("Reloading Prometheus Config")
			req, err := labels.NewRequirement("app", kubeselection.Equals, kubesets.NewString(cfg.PrometheusPodLabel))
			if err != nil {
				log.Fatal(err)
			}

			fmt.Println("Creating Requirement: ", req.String())

			ls := labels.NewSelector()
			ls = ls.Add(*req)

			lo := kubeapi.ListOptions{
				LabelSelector: ls,
			}

			podList, err := c.Pods(string(namespace)).List(lo)
			if err != nil {
				log.Fatal(err)
			}

			for _, pod := range podList.Items {
				if pod.Status.PodIP == "" {
					// This prometheus instance hasnt started yet
					continue
				}
				fmt.Println("Discovered Prometheus instance: ", pod.Status.PodIP)
				resp, err := http.Post("http://"+pod.Status.PodIP+":9090/-/reload", "text/plain", bytes.NewBufferString(""))
				if err != nil {
					log.Fatal(err)
				}
				body, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					log.Fatal(err)
				}
				fmt.Println(body)
				resp.Body.Close()
			}
		}
	}
}
