package web

import "embed"

//go:embed *.html *.css *.js *.svg *.webmanifest vendor/*
var Files embed.FS
