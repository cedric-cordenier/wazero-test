package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed module/testmodule.wasm
var wasm []byte

func NewModule(ctx context.Context, cache wazero.CompilationCache) (*Module, error) {
	cfg := wazero.NewRuntimeConfig().
		WithCompilationCache(cache).
		WithMemoryLimitPages(1000)

	r := wazero.NewRuntimeWithConfig(ctx, cfg)

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	_, err := r.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func(int32, int32, int32, int32) {
			fmt.Printf("called")
		}).
		Export("sendResult").
		Instantiate(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not compile: %w", err)
	}

	compMod, err := r.CompileModule(
		ctx,
		wasm,
	)
	if err != nil {
		return nil, fmt.Errorf("could not compile module: %w", err)
	}

	mod, err := r.InstantiateModule(
		ctx,
		compMod,
		wazero.NewModuleConfig().
			WithArgs("getSpec", "rid", "request"),
	)
	if err != nil {
		return nil, fmt.Errorf("could not instantiate: %w", err)
	}

	return &Module{compMod: compMod, module: mod, runtime: r}, nil
}

func (m *Module) Start(ctx context.Context) error {
	return nil
}

func (m *Module) Close(ctx context.Context) error {
	m.runtime.Close(ctx)
	m.module.Close(ctx)
	m.compMod.Close(ctx)
	return nil
}

type Module struct {
	module  api.Module
	compMod wazero.CompiledModule
	runtime wazero.Runtime
}

func work(cache wazero.CompilationCache, sink chan time.Duration) {
	start := time.Now()
	ctx := context.Background()
	module, err := NewModule(ctx, cache)
	if err != nil {
		log.Fatalf("new module err: %s", err)
	}

	_ = module.Start(ctx)

	module.Close(ctx)

	sink <- time.Now().Sub(start)
	return
}

func main() {
	// we need a compilation cache to prevent ballooning memory usage
	cache := wazero.NewCompilationCache()

	sem := make(chan bool, 100)
	after := time.After(10 * time.Second)

	sink := make(chan time.Duration)

	var sum int64
	var total int64
	for {
		select {

		case <-after:
			f, err := os.Create("mem.out")
			if err != nil {
				log.Fatal("could not create memory profile: ", err)
			}
			defer f.Close() // error handling omitted for example
			runtime.GC()    // get up-to-date statistics
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Fatal("could not write memory profile: ", err)
			}
			f, err = os.Create("cpu.out")
			if err != nil {
				log.Fatal("could not create CPU profile: ", err)
			}
			defer f.Close() // error handling omitted for example
			if err := pprof.StartCPUProfile(f); err != nil {
				log.Fatal("could not start CPU profile: ", err)
			}
			defer pprof.StopCPUProfile()

			fmt.Printf("average duration ms: %s", time.Duration(sum/total)*time.Millisecond)

			return
		case sem <- true:
			go func() {
				defer func() { <-sem }()
				work(cache, sink)
				return
			}()
		case t := <-sink:
			sum += t.Milliseconds()
			total++
		}
	}
}
