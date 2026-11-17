package web

import "embed"

//go:embed *.html *.css *.js *.svg *.webmanifest icons/*.png vendor/*
var Files embed.FS
