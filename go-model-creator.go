package main

import (
	"database/sql"
	"flag"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"log"
	"os"
	"regexp"
	"strings"
	"text/template"
)

type Column struct {
	Name string
	Type string
	Tag  string
}

var (
	dsn         string
	outdir      string
	packageName string

	mayOverrideAll bool = false
)

func main() {
	var err error

	flag.StringVar(&dsn, "d", "", "DSN")
	flag.StringVar(&dsn, "dsn", "", "DSN")
	flag.StringVar(&outdir, "o", "", "Output directory")
	flag.StringVar(&outdir, "out", "", "Output directory")
	flag.StringVar(&packageName, "p", "model", "Package Name")
	flag.StringVar(&packageName, "package", "model", "Package Name")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `
Usage: 
	%s [OPTIONS] ARGS...

Options:`, os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	if dsn == "" || outdir == "" {
		flag.Usage()
		os.Exit(1)
	}

	outdirStat, err := os.Stat(outdir)
	if err == nil {
		if outdirStat.IsDir() == false {
			log.Fatalf("%s is not a directory", outdir)
		}
	} else {
		err = os.MkdirAll(outdir, 0775)
		if err != nil {
			log.Fatalln(err)
		}
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalln(err)
	}
	defer db.Close()

	tables := getTableNames(db)
	for _, table := range tables {
		fmt.Println(table)
		columns, importModules := getTableColumns(db, table)
		exportModel(table, columns, importModules)
	}
}

func extractDbName() string {
	r := regexp.MustCompile(`/([^\?]+)`)
	result := r.FindAllStringSubmatch(dsn, -1)
	if result == nil {
		log.Fatalln("Invalid DSN")
	}
	dbname := result[0][1]

	return dbname
}

func getTableNames(db *sql.DB) []string {
	dbname := extractDbName()

	rows, err := db.Query(`
		SELECT table_name 
		  FROM information_schema.tables 
		 WHERE table_schema = ?
	`, dbname)
	if err != nil {
		log.Fatalln(err)
	}

	var tables = make([]string, 0)
	for rows.Next() {
		var table string
		err = rows.Scan(&table)
		if err != nil {
			log.Fatalln(err)
		}
		tables = append(tables, table)
	}

	return tables
}

func getTableColumns(db *sql.DB, table string) ([]Column, map[string]bool) {
	dbname := extractDbName()

	rows, err := db.Query(`
		SELECT column_name, is_nullable, data_type, column_type
		  FROM information_schema.columns
		 WHERE table_schema = ?
		   AND table_name = ?
	`, dbname, table)
	if err != nil {
		log.Fatalln(err)
	}

	var columns = make([]Column, 0)
	var importModules = make(map[string]bool)
	r := regexp.MustCompile(`^sql\.`)
	for rows.Next() {
		var name, isNullable, dataType, columnType string
		err = rows.Scan(&name, &isNullable, &dataType, &columnType)
		if err != nil {
			log.Fatalln(err)
		}

		var column = Column{
			Name: toCamelCase(name),
			Type: convertType(isNullable, dataType, columnType),
		}
		if column.Type == "*time.Time" {
			importModules["time"] = true
		}
		res := r.Find([]byte(column.Type))
		if res != nil {
			importModules["sql"] = true
		}

		columns = append(columns, column)
	}

	return columns, importModules
}

func exportModel(table string, columns []Column, importModules map[string]bool) {
	filename := fmt.Sprintf("%s/%s.go", outdir, table)

	_, err := os.Stat(filename)
	if err == nil && mayOverrideAll == false {
		mayOverride := confirmOverride(filename)
		if mayOverride == false {
			return
		}
	}

	file, err := os.Create(filename)
	if err != nil {
		log.Fatalf("Failed to open file %s : %s\n", filename, err)
	}
	defer file.Close()

	var param = make(map[string]interface{})
	param["tableName"] = table
	param["tableNameCamel"] = toCamelCase(table)
	param["columns"] = columns
	param["package"] = packageName

	var imports = make([]string, 0)
	for modName, _ := range importModules {
		imports = append(imports, modName)
	}
	param["imports"] = imports

	tmpl := getTemplate()
	t := template.New("t")
	template.Must(t.Parse(tmpl))
	t.Execute(file, param)
}

func confirmOverride(filename string) bool {
	fmt.Printf("File %s already exists. Override? (y/n/a=all): ", filename)

	var b []byte = make([]byte, 2)
	for {
		os.Stdin.Read(b)
		c := strings.ToLower(string(b[0]))
		switch c {
		case "n":
			return false
			break
		case "y":
			return true
			break
		case "a":
			mayOverrideAll = true
			return true
			break
		}
	}
}

func toCamelCase(str string) string {
	splitted := strings.Split(str, "_")
	camel := ""
	for _, word := range splitted {
		camel = camel + strings.Title(word)
	}
	return camel
}

func convertType(isNullable, dataType, columnType string) string {
	r := regexp.MustCompile(`unsigned$`)
	result := r.Find([]byte(columnType))
	isUnsigned := false
	if result != nil {
		isUnsigned = true
	}

	switch dataType {
	case "int":
		if isNullable == "YES" {
			return "sql.NullInt64"
		} else {
			if isUnsigned {
				return "uint64"
			} else {
				return "int64"
			}
		}
	case "smallint":
		if isNullable == "YES" {
			return "sql.NullInt64"
		} else {
			if isUnsigned {
				return "uint16"
			} else {
				return "int16"
			}
		}
	case "tinyint":
		if isNullable == "YES" {
			return "sql.NullInt64"
		} else {
			if isUnsigned {
				return "uint8"
			} else {
				return "int8"
			}
		}
	case "decimal":
		if isNullable == "YES" {
			return "sql.NullFloat64"
		} else {
			return "float64"
		}
	case "varchar":
		return asString(isNullable)
	case "char":
		return asString(isNullable)
	case "text":
		return asString(isNullable)
	case "datetime":
		return asTime()
	case "date":
		return asTime()
	case "timestamp":
		return asTime()
	default:
		log.Fatalf("Unknown type: %s\n", dataType)
	}

	return ""
}

func asString(isNullable string) string {
	if isNullable == "YES" {
		return "sql.NullString"
	} else {
		return "string"
	}
}

func asTime() string {
	return "*time.Time"
}

func getTemplate() string {
	return `package {{.package}}

import (
{{range .imports}}    "{{.}}"
{{end}}
)

type {{.tableNameCamel}} struct {
{{range .columns}}    {{.Name}} {{.Type}} {{.Tag}}
{{end}}
}

func (r *{{.tableNameCamel}}) TableName() string {
	return "{{.tableName}}"
}

`
}
