package httpchannel

import (
	"embed"
	"io/fs"
)

//go:embed assets/*
var uiAssets embed.FS

func embeddedUIAssets() fs.FS {
	sub, err := fs.Sub(uiAssets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}
