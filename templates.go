package main

import (
	"embed"
	_ "embed"
)

// fs is a file system that contains all templates.
//go:embed tpl
var fs embed.FS
