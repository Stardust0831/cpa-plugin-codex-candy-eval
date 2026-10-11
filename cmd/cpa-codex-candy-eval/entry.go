//go:build !homepanel

package main

import "cpa-codex-candy-eval/internal/plugin"

func handleMethod(method string, request []byte) []byte { return plugin.HandleMethod(method, request) }
func shutdown()                                         { plugin.Quiesce() }
