//go:build !windows

package main

import "fmt"

func main() { fmt.Println("conpty runs on Windows only") }
