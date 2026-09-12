package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"io"
	"os"
)

func decodeTDBBinding(data []byte, batch *kentity.TDBBindingBatch) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(batch); err != nil {
		return errors.New("invalid ID-only shadow snapshot JSON")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return errors.New("multiple JSON values refused")
	}
	return nil
}
func tdbShadowCommand(ctx context.Context, pool *pgxpool.Pool, args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("tdb-shadow-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	apply := flags.Bool("apply", false, "apply reviewed bounded ID snapshot")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid shadow command flags")
	}
	if *apply && (os.Getenv("KDB_TDB_SHADOW_ENABLED") != "1" || os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1") {
		return errors.New("shadow intake feature gate is off")
	}
	data, err := io.ReadAll(io.LimitReader(in, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("shadow snapshot exceeds input bound")
	}
	var batch kentity.TDBBindingBatch
	if err = decodeTDBBinding(data, &batch); err != nil {
		return err
	}
	report, err := (&kentity.Store{Pool: pool}).ImportTDBBindings(ctx, "kdb:tdb-shadow-cli", batch, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}
