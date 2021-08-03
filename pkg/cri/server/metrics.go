package server

import (
	_ "fmt"

	metrics "github.com/docker/go-metrics"
)

const (
	RunPodSandboxDurationKey  = "run_pod_sandbox_duration"
	StopPodSandboxDurationKey = "stop_pod_sandbox_duration"
	StopContainerDurationKey  = "stop_container_duration"
)

var (
	RunPodSandboxDuration  metrics.LabeledTimer
	StopPodSandboxDuration metrics.LabeledTimer
	StopContainerDuration  metrics.Timer
)

func init() {
	ns := metrics.NewNamespace("vci", "cri", nil)
	RunPodSandboxDuration = ns.NewLabeledTimer(RunPodSandboxDurationKey,
		`Duration in seconds of cri RunPodSandbox. Broken down by stage.
		image: pull and convert the image.
		network: set up the network, including calling cni plugin.
		container: create a container in memory, which DOES NOT include calling shim.
		file: setup the rootfs.
		task: create and start a task, which includes calling the nri plugin and the shim.`,
		"stage")
	StopPodSandboxDuration = ns.NewLabeledTimer(StopPodSandboxDurationKey,
		`Duration in seconds of cri StopPodSandbox. Broken down by stage.
		network: tear down the network, including calling cni plugin.
		container: stop all containers in the sandbox, including calling shim.
		sandbox: stop the sanbox container, including calling shim.
		file: remove files.`,
		"stage")
	StopContainerDuration = ns.NewTimer(StopContainerDurationKey, "Duration in seconds of cri StopContainer.")
	metrics.Register(ns)
}
