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
	"time"
)

func decodeTDBObservations(data []byte, batch *kentity.TDBObservationBatch) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(batch); err != nil {
		return errors.New("invalid ID observation JSON")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple JSON values refused")
	}
	return nil
}
func tdbObservationCommand(ctx context.Context, pool *pgxpool.Pool, args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("tdb-shadow-observe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	apply := flags.Bool("apply", false, "apply source observations")
	list := flags.Bool("list", false, "oldest known source identifiers only")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (*apply && *list) {
		return errors.New("invalid observer flags")
	}
	if *apply && (os.Getenv("KDB_TDB_OBSERVER_ENABLED") != "1" || os.Getenv("KDB_TDB_SHADOW_ENABLED") != "1" || os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1") {
		return errors.New("TDB observer feature gate is off")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	s := &kentity.Store{Pool: pool}
	if *list {
		targets, err := s.TDBObservationTargets(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(targets)
	}
	raw, err := io.ReadAll(io.LimitReader(in, 65537))
	if err != nil || len(raw) > 65536 {
		return errors.New("ID observation input exceeds bound")
	}
	var batch kentity.TDBObservationBatch
	if err = decodeTDBObservations(raw, &batch); err != nil {
		return err
	}
	report, err := s.ObserveTDBBindings(ctx, batch, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}
