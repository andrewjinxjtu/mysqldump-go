// Package mysqldump ignore_security_alert_file SQL
package mysqldump

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const BufferSize = 1 << 20

type SafeWriter struct {
	*bufio.Writer
}

func NewSafeWriterWithSize(writer io.Writer, size int) *SafeWriter {
	return &SafeWriter{bufio.NewWriterSize(writer, size)}
}

// WriteString If the next write buffer will overflow, refresh it first
func (w *SafeWriter) WriteString(s string) (int, error) {
	l := len(s)
	if w.Available() < l {
		_ = w.Flush()
	}
	return w.Writer.WriteString(s)
}

type dumpOption struct {
	// export data
	isData bool
	// specified database
	dbs []string
	// export all databases
	isAllDB bool
	// specified table
	tables []string
	// export all tables
	isAllTable bool
	// drop table after dumped
	isDropTable bool
	// export table DDL
	isDumpTable bool
	// where condition in DML
	where string
	// export destination, output to the console by default
	writer io.Writer
	// export primary key ID
	withoutPrimaryID bool
}

type DumpOption func(*dumpOption)

func WithData() DumpOption {
	return func(option *dumpOption) {
		option.isData = true
	}
}

// WithDBs mutually exclusive with WithAllDatabases and WithAllDatabases has higher priority
func WithDBs(databases ...string) DumpOption {
	return func(option *dumpOption) {
		option.dbs = databases
	}
}

func WithAllDatabases() DumpOption {
	return func(option *dumpOption) {
		option.isAllDB = true
	}
}

// WithTables mutually exclusive with WithAllTable and WithAllTable has higher priority
func WithTables(tables ...string) DumpOption {
	return func(option *dumpOption) {
		option.tables = tables
	}
}

func WithAllTables() DumpOption {
	return func(option *dumpOption) {
		option.isAllTable = true
	}
}

func WithDropTable() DumpOption {
	return func(option *dumpOption) {
		option.isDropTable = true
	}
}

func WithDumpTable() DumpOption {
	return func(option *dumpOption) {
		option.isDumpTable = true
	}
}

func WithWhere(where string) DumpOption {
	return func(option *dumpOption) {
		option.where = where
	}
}

func WithWriter(writer io.Writer) DumpOption {
	return func(option *dumpOption) {
		option.writer = writer
	}
}

func WithoutPrimaryID(withoutPrimaryID bool) DumpOption {
	return func(option *dumpOption) {
		option.withoutPrimaryID = withoutPrimaryID
	}
}

func Dump(dns string, opts ...DumpOption) error {

	start := time.Now()
	log.Printf("[info] [dump] start at %s\n", start.Format("2006-01-02 15:04:05"))

	defer func() {
		end := time.Now()
		log.Printf("[info] [dump] end at %s, cost %s\n", end.Format("2006-01-02 15:04:05"), end.Sub(start))
	}()

	var err error

	var o dumpOption

	for _, opt := range opts {
		opt(&o)
	}

	// db in dsn by default
	if len(o.dbs) == 0 {
		dbName, err := GetDBNameFromDNS(dns)
		if err != nil {
			log.Printf("[error] %v \n", err)
			return err
		}
		o.dbs = []string{
			dbName,
		}
	}

	// export all tables by default
	if len(o.tables) == 0 {
		o.isAllTable = true
	}

	// output to the console by default
	if o.writer == nil {
		o.writer = os.Stdout
	}

	buf := NewSafeWriterWithSize(o.writer, BufferSize)
	defer func() {
		_ = buf.Flush()
	}()

	_, _ = buf.WriteString("-- ----------------------------\n")
	_, _ = buf.WriteString("-- MySQL Database Dump\n")
	_, _ = buf.WriteString("-- Start Time: " + start.Format("2006-01-02 15:04:05") + "\n")
	_, _ = buf.WriteString("-- ----------------------------\n")
	_, _ = buf.WriteString("\n\n")

	db, err := sql.Open("mysql", dns)
	if err != nil {
		log.Printf("[error] %v \n", err)
		return err
	}
	defer func() {
		_ = db.Close()
	}()

	var dbs []string
	if o.isAllDB {
		dbs, err = getDBs(db)
		if err != nil {
			log.Printf("[error] %v \n", err)
			return err
		}
	} else {
		dbs = o.dbs
	}

	for _, dbStr := range dbs {
		_, err = db.Exec(fmt.Sprintf("USE `%s`", dbStr))
		if err != nil {
			log.Printf("[error] %v \n", err)
			return err
		}

		var tables []string
		if o.isAllTable {
			tmp, err := getAllTables(db)
			if err != nil {
				log.Printf("[error] %v \n", err)
				return err
			}
			tables = tmp
		} else {
			tables = o.tables
		}

		_, _ = buf.WriteString(fmt.Sprintf("USE `%s`;\n", dbStr))

		for _, table := range tables {

			if o.isDropTable {
				_, _ = buf.WriteString(fmt.Sprintf("DROP TABLE IF EXISTS `%s`;\n", table))
			}

			if o.isDumpTable {
				err = writeTableStruct(db, table, buf)
				if err != nil {
					log.Printf("[error] %v \n", err)
					return err
				}
			}

			if o.isData {
				where := o.where
				withoutPrimaryID := o.withoutPrimaryID
				err = writeTableData(db, table, where, buf, withoutPrimaryID)
				if err != nil {
					log.Printf("[error] %v \n", err)
					return err
				}
			}
		}
	}

	_, _ = buf.WriteString("-- ----------------------------\n")
	_, _ = buf.WriteString("-- Dump completed\n")
	_, _ = buf.WriteString("-- Cost Time: " + time.Since(start).String() + "\n")
	_, _ = buf.WriteString("-- ----------------------------\n")
	_ = buf.Flush()

	return nil
}

func getCreateTableSQL(db *sql.DB, table string) (string, error) {
	var createTableSQL string
	err := db.QueryRow(fmt.Sprintf("SHOW CREATE TABLE `%s`", table)).Scan(&table, &createTableSQL) // ignore_security_alert_wait_for_fix SQL
	if err != nil {
		return "", err
	}

	createTableSQL = strings.Replace(createTableSQL, "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1)
	return createTableSQL, nil
}

func getDBs(db *sql.DB) ([]string, error) {
	var dbs []string
	rows, err := db.Query("SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var db string
		err = rows.Scan(&db)
		if err != nil {
			return nil, err
		}
		dbs = append(dbs, db)
	}
	return dbs, nil
}

func getAllTables(db *sql.DB) ([]string, error) {
	var tables []string
	rows, err := db.Query("SHOW TABLES")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var table string
		err = rows.Scan(&table)
		if err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, nil
}

func writeTableStruct(db *sql.DB, table string, buf *SafeWriter) error {
	_, _ = buf.WriteString("-- ----------------------------\n")
	_, _ = buf.WriteString(fmt.Sprintf("-- Table structure for %s\n", table))
	_, _ = buf.WriteString("-- ----------------------------\n")

	createTableSQL, err := getCreateTableSQL(db, table)
	if err != nil {
		log.Printf("[error] %v \n", err)
		return err
	}
	_, _ = buf.WriteString(createTableSQL)
	_, _ = buf.WriteString(";")

	_, _ = buf.WriteString("\n\n")
	_, _ = buf.WriteString("\n\n")
	return nil
}

func writeTableData(db *sql.DB, table, where string, buf *SafeWriter, withoutPrimaryID bool) error {
	var (
		writeCh = make(chan string, 1)
		done    = make(chan struct{}, 1)
	)

	_, _ = buf.WriteString("-- ----------------------------\n")
	_, _ = buf.WriteString(fmt.Sprintf("-- Records of %s\n", table))
	_, _ = buf.WriteString("-- ----------------------------\n")

	lineRows, err := db.Query(func(table, where string) string {
		dml := fmt.Sprintf("SELECT * FROM `%s`", table)
		if strings.TrimSpace(where) != "" {
			dml = fmt.Sprintf("%s where %s", dml, where)
		}
		return dml
	}(table, where)) // ignore_security_alert_wait_for_fix SQL
	if err != nil {
		log.Printf("[error] %v \n", err)
		return err
	}
	defer func() {
		_ = lineRows.Close()
	}()

	var columns []string
	columns, err = lineRows.Columns()
	if err != nil {
		log.Printf("[error] %v \n", err)
		return err
	}
	columnTypes, err := lineRows.ColumnTypes()
	if err != nil {
		log.Printf("[error] %v \n", err)
		return err
	}

	go writeViaBuf(buf, writeCh, done)

	var row []interface{}
	var rowPointers []interface{}
	var dml string

	for lineRows.Next() {
		row = make([]interface{}, len(columns))
		rowPointers = make([]interface{}, len(columns))
		for i := range columns {
			rowPointers[i] = &row[i]
		}
		err = lineRows.Scan(rowPointers...)
		if err != nil {
			log.Printf("[error] %v \n", err)
			return err
		}

		dml = "INSERT INTO `" + table + "` VALUES ("

		for i, col := range row {
			if col == nil {
				dml += "NULL"
			} else {
				Type := columnTypes[i].DatabaseTypeName()
				columnName := columnTypes[i].Name()
				Type = strings.Replace(Type, "UNSIGNED", "", -1)
				Type = strings.Replace(Type, " ", "", -1)

				switch Type {
				case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT":
					if bs, ok := col.([]byte); ok {
						if withoutPrimaryID && columnName == "id" {
							dml += "0"
							break
						}
						dml += string(bs)
					} else {
						dml += fmt.Sprintf("%d", col)
					}
				case "FLOAT", "DOUBLE":
					if bs, ok := col.([]byte); ok {
						dml += string(bs)
					} else {
						dml += fmt.Sprintf("%f", col)
					}
				case "DECIMAL", "DEC":
					dml += fmt.Sprintf("%s", col)

				case "DATE":
					t, ok := col.(time.Time)
					if !ok {
						log.Println("DATE type conversion error")
						return err
					}
					dml += fmt.Sprintf("'%s'", t.Format("2006-01-02"))
				case "DATETIME":
					t, ok := col.(time.Time)
					if !ok {
						log.Println("DATETIME type conversion error")
						return err
					}
					dml += fmt.Sprintf("'%s'", t.Format("2006-01-02 15:04:05"))
				case "TIMESTAMP":
					t, ok := col.(time.Time)
					if !ok {
						log.Println("TIMESTAMP type conversion error")
						return err
					}
					dml += fmt.Sprintf("'%s'", t.Format("2006-01-02 15:04:05"))
				case "TIME":
					t, ok := col.([]byte)
					if !ok {
						log.Println("TIME type conversion error")
						return err
					}
					dml += fmt.Sprintf("'%s'", string(t))
				case "YEAR":
					t, ok := col.([]byte)
					if !ok {
						log.Println("YEAR type conversion error")
						return err
					}
					dml += string(t)
				case "CHAR", "VARCHAR", "TINYTEXT", "TEXT", "MEDIUMTEXT", "LONGTEXT":
					dml += fmt.Sprintf("'%s'", strings.Replace(fmt.Sprintf("%s", col), "'", "''", -1))
				case "BIT", "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB":
					dml += fmt.Sprintf("0x%X", col)
				case "ENUM", "SET":
					dml += fmt.Sprintf("'%s'", col)
				case "BOOL", "BOOLEAN":
					if col.(bool) {
						dml += "true"
					} else {
						dml += "false"
					}
				case "JSON":
					dml += fmt.Sprintf("'%s'", col)
				default:
					log.Printf("unsupported type: %s", Type)
					return fmt.Errorf("unsupported type: %s", Type)
				}
			}
			if i < len(row)-1 {
				dml += ","
			}
		}

		dml += ");\n"
		writeCh <- dml
	}

	_, _ = buf.WriteString("\n\n")

	done <- struct{}{}

	return nil
}

func writeViaBuf(writer *SafeWriter, writeCh chan string, done chan struct{}) {
	for {
		select {
		case data := <-writeCh:
			_, _ = writer.WriteString(data)
		case <-done:
			_ = writer.Flush()
			return
		}
	}
}
