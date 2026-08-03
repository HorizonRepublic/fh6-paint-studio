package main

// backendPref carries the -backend flag to newBackend. Only the allgpu build has a choice to make;
// single-backend builds compile the flag in and ignore it, so the same command line works on any
// build. Empty = the build's default order (Vulkan first).
var backendPref string
