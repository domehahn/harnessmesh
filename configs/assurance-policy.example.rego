package harnessmesh

default allow := true

allow if {
	input.operation != "production_deploy"
}

allow if {
	input.operation == "production_deploy"
	input.agent == "release-operator"
}
