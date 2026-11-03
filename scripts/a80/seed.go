// Seeds A75's made-up pages into a database, for scripts/a80-size.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RIKIKU/PagerArchive/internal/seeddata"
)

func main() {
	db := flag.String("db", "", "database URL")
	pages := flag.Int("pages", seeddata.A75Pages, "pages to seed")
	flag.Parse()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *db)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	seeded, built, err := seeddata.Seed(ctx, pool, *pages)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("seeded %d pages in %s, search index built in %s\n", *pages, seeded.Round(1e9), built.Round(1e9))
}
