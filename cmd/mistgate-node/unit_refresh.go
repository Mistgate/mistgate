package main

import "context"

type unitRefreshConfig struct {
	underSystemd bool
	reason       string
	busy         func(context.Context) error
	reserve      func(context.Context) (func(started bool), error)
	launch       func(context.Context, func()) error
}
