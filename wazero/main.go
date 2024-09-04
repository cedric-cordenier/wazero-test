package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed module/testmodule.wasm
var wasm []byte

func NewModule(ctx context.Context, runtime wazero.Runtime, compMod wazero.CompiledModule) (*Module, error) {
	mod, err := runtime.InstantiateModule(
		ctx,
		compMod,
		wazero.NewModuleConfig().
			WithArgs("getSpec", "rid", "request").
			WithName(""),
	)
	if err != nil {
		return nil, fmt.Errorf("could not instantiate: %w", err)
	}

	return &Module{compMod: compMod, module: mod, runtime: runtime}, nil
}

func (m *Module) Start(ctx context.Context) error {
	return nil
}

func (m *Module) Close(ctx context.Context) error {
	m.module.Close(ctx)
	return nil
}

type Module struct {
	module  api.Module
	compMod wazero.CompiledModule
	runtime wazero.Runtime
}

func work(runtime wazero.Runtime, compMod wazero.CompiledModule, sink chan time.Duration) {
	start := time.Now()
	ctx := context.Background()
	module, err := NewModule(ctx, runtime, compMod)
	if err != nil {
		log.Fatalf("new module err: %s", err)
	}

	_ = module.Start(ctx)

	module.Close(ctx)

	select {
	case sink <- time.Now().Sub(start):
	default:
	}
	return
}

func main() {
	ctx := context.Background()
	cfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(600)

	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	defer r.Close(ctx)

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	hostModule, err := r.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func(int32, int32, int32, int32) {
			fmt.Printf("called")
		}).
		Export("sendResult").
		Instantiate(ctx)
	if err != nil {
		panic(fmt.Errorf("could not compile: %w", err))
	}

	defer hostModule.Close(ctx)

	compMod, err := r.CompileModule(
		ctx,
		wasm,
	)
	if err != nil {
		panic(fmt.Errorf("could not compile module: %w", err))
	}

	defer compMod.Close(ctx)

	sem := make(chan bool, 100)
	after := time.After(30 * time.Second)

	sink := make(chan time.Duration)

	var sum int64
	var total int64
	var wg sync.WaitGroup
	for {
		select {
		case <-after:
			wg.Wait()
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
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				work(r, compMod, sink)
				return
			}()
		case t := <-sink:
			sum += t.Milliseconds()
			total++
		}
	}
}
