//go:build !cuda && !vulkan && !allgpu

package main

func backendOptions() []string { return []string{"CPU"} }
