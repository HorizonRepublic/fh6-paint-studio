//go:build cuda && !allgpu

package main

func backendOptions() []string { return []string{"CUDA"} }
