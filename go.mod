module github.com/antresol-app/antresol-addon-airplay

go 1.25

toolchain go1.27.1

// the addonkit release the public build uses (go.sum keeps its two h1
// lines); in the core repository the replace makes them unused, so go mod
// tidy there would drop them
require github.com/antresol-app/antresol/addonkit v0.3.3
