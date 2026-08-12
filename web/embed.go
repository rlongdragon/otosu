// Package web holds the browser client, embedded into the binary so the server
// ships as a single file.
package web

import "embed"

//go:embed index.html app.js seal.js
var FS embed.FS
