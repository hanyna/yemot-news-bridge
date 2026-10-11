//go:build !windows

package main

// keepSystemAwake רלוונטי רק לווינדוס (מצב patriots רץ על המחשב של בעל הקו).
func keepSystemAwake() {}
