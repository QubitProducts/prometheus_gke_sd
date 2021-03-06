package main

type RelabelConfig struct {
	SourceLabels []string `yaml:"source_labels,flow"`
	Seperator    string   `yaml:"seperator,omitempty"`
	Regex        string   `yaml:"regex,omitempty"`
	Modulus      uint64   `yaml:"modulus,omitempty"`
	TargetLabel  string   `yaml:"target_label,omitempty"`
	Replacement  string   `yaml:"replacement,omitempty"`
	Action       string   `yaml:"action,omitempty"`
}

func GetRoles() map[string][]RelabelConfig {
	/*
				By the time you find this, it'll be too late.
				              ___.-~"~-._   __....__
		            .'    `    \ ~"~        ``-.
		           /` _      )  `\              `\
		          /`  a)    /     |               `\
		         :`        /      |                 \
		    <`-._|`  .-.  (      /   .            `;\\
		     `-. `--'_.'-.;\___/'   .      .       | \\
		  _     /:--`     |        /     /        .'  \\
		 ("\   /`/        |       '     '         /    :`;
		 `\'\_/`/         .\     /`~`=-.:        /     ``
		   `._.'          /`\    |      `\      /(
		                 /  /\   |        `Y   /  \
		           jgs  J  /  Y  |         |  /`\  \
		               /  |   |  |         |  |  |  |
		              "---"  /___|        /___|  /__|
		                     '"""         '"""  '"""
				         An Elephant never forgets.
	*/
	return map[string][]RelabelConfig{
		"apiserver": {},
		"node": {
			{
				Action: "labelmap",
				Regex:  "__meta_kubernetes_node_label_(.+)",
			},
			{
				SourceLabels: []string{
					"__address__",
				},
				Action:      "replace",
				Regex:       "([\\d\\.]+):([\\d]+)",
				TargetLabel: "__address__",
				Replacement: "$1:10255",
			},
		},
		"endpoint": {
			{
				SourceLabels: []string{
					"__meta_kubernetes_service_annotation_prometheus_io_scrape",
				},
				Action: "keep",
				Regex:  "true",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_service_annotation_prometheus_io_scheme",
				},
				Action:      "replace",
				Regex:       "(https?)",
				TargetLabel: "__scheme__",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_service_annotation_prometheus_io_path",
				},
				Action:      "replace",
				Regex:       "(.+)",
				TargetLabel: "__metrics_path__",
			},
			{
				SourceLabels: []string{
					"__address__",
					"__meta_kubernetes_service_annotation_prometheus_io_port",
				},
				Action:      "replace",
				Regex:       "(.+)(?::\\d+);(\\d+)",
				TargetLabel: "__address__",
				Replacement: "$1:$2",
			},
			{
				Action: "labelmap",
				Regex:  "__meta_kubernetes_endpoint_label_(.+)",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_service_namespace",
				},
				Action:      "replace",
				TargetLabel: "kubernetes_namespace",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_service_name",
				},
				Action:      "replace",
				TargetLabel: "kubernetes_name",
			},
		},
		"service": {
			{
				SourceLabels: []string{
					"__meta_kubernetes_service_annotation_prometheus_io_probe",
				},
				Action: "keep",
				Regex:  "true",
			},
			{
				SourceLabels: []string{
					"__address__",
				},
				Regex:       "(.*)(:80)?",
				TargetLabel: "__param_target",
				Replacement: "${1}",
			},
			{
				SourceLabels: []string{
					"__param_target",
				},
				Regex:       "(.*)",
				TargetLabel: "instance",
				Replacement: "${1}",
			},
			{
				SourceLabels: []string{},
				Regex:        ".*",
				TargetLabel:  "__address",
				Replacement:  "blackbox:9115",
			},
			{
				Action: "labelmap",
				Regex:  "__meta_kubernetes_service_label_(.+)",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_service_namespace",
				},
				TargetLabel: "kubernetes_namespace",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_service_name",
				},
				TargetLabel: "kubernetes_name",
			},
		},
		"pod": {
			{
				SourceLabels: []string{
					"__meta_kubernetes_pod_annotation_prometheus_io_scrape",
				},
				Action: "keep",
				Regex:  "true",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_pod_annotation_prometheus_io_path",
				},
				Action:      "replace",
				Regex:       "(.+)",
				TargetLabel: "__metrics_path__",
			},
			{
				SourceLabels: []string{
					"__address__",
					"__meta_kubernetes_pod_annotation_prometheus_io_port",
				},
				Action:      "replace",
				Regex:       "(.+):(?:\\d+);(\\d+)",
				Replacement: "${1}:${2}",
				TargetLabel: "__address__",
			},
			{
				Action: "labelmap",
				Regex:  "__meta_kubernetes_pod_label_(.+)",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_pod_namespace",
				},
				Action:      "replace",
				TargetLabel: "kubernetes_namespace",
			},
			{
				SourceLabels: []string{
					"__meta_kubernetes_pod_name",
				},
				Action:      "replace",
				TargetLabel: "kubernetes_pod_name",
			},
		},
	}
}
