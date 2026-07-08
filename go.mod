module github.com/zoo/telegram-anthropic-chat

go 1.26.4

require (
	github.com/Mad-Pixels/goldmark-tgmd v0.0.10
	github.com/anthropics/anthropic-sdk-go v1.56.0
	github.com/go-telegram/bot v1.22.0
	github.com/google/uuid v1.6.0
	github.com/iamwavecut/go-tavily v0.0.0-20250618204438-bc055052ee53
	golang.org/x/image v0.43.0
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/yuin/goldmark v1.8.2 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/sync v0.16.0 // indirect
)

// Use a local fork of goldmark-tgmd with proper ordered-list rendering
// (the upstream replaces ordered-list numbers with bullet characters).
replace github.com/Mad-Pixels/goldmark-tgmd => ./third_party/goldmark-tgmd
