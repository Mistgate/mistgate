package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

type schemaObject struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Table string `json:"tbl_name"`
	SQL   string `json:"sql"`
}

func normalizedSchema(ctx context.Context, db *sql.DB) ([]byte, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_master
		WHERE name NOT GLOB 'sqlite_*'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		var statement sql.NullString
		if err := rows.Scan(&object.Type, &object.Name, &object.Table, &statement); err != nil {
			return nil, err
		}
		if statement.Valid {
			object.SQL = strings.Join(strings.Fields(statement.String), " ")
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].Type != objects[j].Type {
			return objects[i].Type < objects[j].Type
		}
		if objects[i].Name != objects[j].Name {
			return objects[i].Name < objects[j].Name
		}
		if objects[i].Table != objects[j].Table {
			return objects[i].Table < objects[j].Table
		}
		return objects[i].SQL < objects[j].SQL
	})
	return json.MarshalIndent(objects, "", "  ")
}
