package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

const usageText = "用法: xero-mirror <sync|serve|auth>\n" +
	"  sync [-month YYYY-MM] [-entity NAME]  依 cursor 增量、回補單月或只同步指定實體（可併用）\n" +
	"  serve                  cron + webhook 常駐\n" +
	"  auth                   一次性授權（token 落地 MySQL）\n"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := dispatch(ctx, os.Args[1:])
	if errors.Is(err, context.Canceled) || errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("執行失敗", "err", err)
		os.Exit(1)
	}
}

func dispatch(ctx context.Context, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	switch name {
	case "sync":
		return runSync(ctx, args[1:])
	case "serve":
		return runServe(ctx, args[1:])
	case "auth":
		return runAuth(ctx, args[1:])
	}
	printUsage()
	if name == "" {
		return fmt.Errorf("未指定 subcommand")
	}
	if name == "-h" || name == "--help" || name == "help" {
		return nil
	}
	return fmt.Errorf("未知的 subcommand %q", name)
}

func printUsage() {
	fmt.Fprint(os.Stderr, usageText)
}
