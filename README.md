# Gist Play

A zero-dependency directory of single-page HTML apps.

## Structure
- `src/`: Source apps. Individual folders containing `index.html`.
- `helpers/`: Shared `header.html` and `footer.html`.
- `dist/`: Generated build output.

## Metadata
Each `index.html` requires an `APP-META` block:
```html
<!-- APP-META
Title: My App
Description: Description...
Category: Tools
Status: published
Updated: 1712444281
-->
```

## Usage
Requires Go.
- `go run gist.go build`: Generate `dist/`.
- `go run gist.go preview`: Start local server.
- `go run gist.go update-metadata`: Update timestamps.

## Deployment
Automated via GitHub Actions.
