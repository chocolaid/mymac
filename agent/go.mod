module mymac-agent

go 1.25.0

require (
	github.com/denisbrodbeck/machineid v1.0.1
	mackit v0.0.0
)

require golang.org/x/sys v0.42.0 // indirect

replace mackit v0.0.0 => ./mackit
