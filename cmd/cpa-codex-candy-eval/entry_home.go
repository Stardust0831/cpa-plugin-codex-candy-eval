//go:build homepanel

package main

import "cpa-codex-candy-eval/internal/homeeval"

var homePlugin homeeval.StorePlugin

func handleMethod(method string, request []byte) []byte {
	return homePlugin.HandleMethod(method, request)
}
func shutdown() { homePlugin.Close() }
