package api

import "time"

// Variables abstraites pour faciliter le mocking dans les tests si besoin.
var (
	echoNow   = time.Now
	echoSince = time.Since
)
