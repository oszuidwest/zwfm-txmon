package main

import "embed"

// webFS holds the web interface: pages, stylesheet, script and icon.
//
//go:embed web
var webFS embed.FS
