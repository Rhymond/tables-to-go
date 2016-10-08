package tablestogo

import (
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"

	"bytes"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

var (
	db *sqlx.DB

	SupportedDbTypes       = []string{"pg", "mysql"}
	SupportedOutputFormats = []string{"c", "o"}

	DbTypeToDriverMap = map[string]string{
		"pg":    "postgres",
		"mysql": "mysql",
	}

	DbDefaultPorts = map[string]string{
		"pg":    "5432",
		"mysql": "3306",
	}

	settings *Settings
)

type Settings struct {
	Verbose        bool
	DbType         string
	User           string
	Pswd           string
	DbName         string
	Schema         string
	Host           string
	Port           string
	OutputFilePath string
	OutputFormat   string
	PackageName    string
	Prefix         string
	Suffix         string

	IsMastermindStructable         bool
	IsMastermindStructableOnly     bool
	IsMastermindStructableRecorder bool
}

func NewSettings() *Settings {
	return &Settings{
		Verbose:                        false,
		DbType:                         "pg",
		User:                           "postgres",
		Pswd:                           "",
		DbName:                         "postgres",
		Schema:                         "public",
		Host:                           "127.0.0.1",
		Port:                           "5432",
		OutputFilePath:                 "./output",
		OutputFormat:                   "c",
		PackageName:                    "dto",
		Prefix:                         "",
		Suffix:                         "",
		IsMastermindStructable:         false,
		IsMastermindStructableOnly:     false,
		IsMastermindStructableRecorder: false,
	}
}

func Run(s *Settings) (err error) {

	err = VerifySettings(s)
	if err != nil {
		return err
	}
	settings = s

	err = connect()
	if err != nil {
		return err
	}
	defer db.Close()

	var database Database
	generalDatabase := &GeneralDatabase{
		db:       db,
		Settings: s,
	}

	switch s.DbType {
	case "mysql":
		database = &MySQLDatabase{
			GeneralDatabase: generalDatabase,
		}
	default: // pg
		database = &PostgreDatabase{
			GeneralDatabase: generalDatabase,
		}
	}

	return run(database)
}

func VerifySettings(settings *Settings) (err error) {

	if !IsStringInSlice(settings.DbType, SupportedDbTypes) {
		return errors.New(fmt.Sprintf("type of database %q not supported! %v", settings.DbType, SupportedDbTypes))
	}

	if !IsStringInSlice(settings.OutputFormat, SupportedOutputFormats) {
		return errors.New(fmt.Sprintf("output format %q not supported! %v", settings.OutputFormat, SupportedOutputFormats))
	}

	if err = verifyOutputPath(settings.OutputFilePath); err != nil {
		return err
	}

	if settings.OutputFilePath, err = prepareOutputPath(settings.OutputFilePath); err != nil {
		return err
	}

	if settings.Port == "" {
		settings.Port = DbDefaultPorts[settings.DbType]
	}

	if settings.PackageName == "" {
		return errors.New("name of package can not be empty!")
	}

	return err
}

func verifyOutputPath(outputFilePath string) (err error) {

	info, err := os.Stat(outputFilePath)

	if os.IsNotExist(err) {
		return errors.New(fmt.Sprintf("output file path %q does not exists!", outputFilePath))
	}

	if !info.Mode().IsDir() {
		return errors.New(fmt.Sprintf("output file path %q is not a directory!", outputFilePath))
	}

	return err
}

func prepareOutputPath(ofp string) (outputFilePath string, err error) {
	outputFilePath, err = filepath.Abs(ofp + "/")
	return outputFilePath, err
}

func connect() (err error) {
	db, err = sqlx.Connect(DbTypeToDriverMap[settings.DbType], prepareDataSourceName())
	if err != nil {
		usingPswd := "no"
		if settings.Pswd != "" {
			usingPswd = "yes"
		}
		return errors.New(
			fmt.Sprintf("Connection to Database (type=%q, user=%q, database=%q, host='%v:%v' (using password: %v) failed:\r\n%v",
				settings.DbType, settings.User, settings.DbName, settings.Host, settings.Port, usingPswd, err))
	}
	return db.Ping()
}

func prepareDataSourceName() (dataSourceName string) {
	switch settings.DbType {
	case "mysql":
		dataSourceName = fmt.Sprintf("%v:%v@tcp(%v:%v)/%v", settings.User, settings.Pswd, settings.Host, settings.Port, settings.DbName)
	default: // pg
		dataSourceName = fmt.Sprintf("host=%v port=%v user=%v dbname=%v password=%v sslmode=disable",
			settings.Host, settings.Port, settings.User, settings.DbName, settings.Pswd)
	}
	return dataSourceName
}

func run(db Database) (err error) {

	fmt.Printf("running for %q...\r\n", settings.DbType)

	tables, err := db.GetTables()

	if err != nil {
		return err
	}

	if settings.Verbose {
		fmt.Printf("> count of tables: %v\r\n", len(tables))
	}

	err = db.PrepareGetColumnsOfTableStmt()

	if err != nil {
		return err
	}

	for _, table := range tables {

		if settings.Verbose {
			fmt.Printf("> processing table %q\r\n", table.TableName)
		}

		err = db.GetColumnsOfTable(table)

		if err != nil {
			return err
		}

		err = createStructOfTable(table)

		if err != nil {
			if settings.Verbose {
				fmt.Printf(">Error at createStructOfTable(%v)\r\n", table.TableName)
			}
			return err
		}
	}

	fmt.Println("done!")

	return err
}

// TODO refactor to clean code
func createStructOfTable(table *Table) (err error) {

	var buffer, colBuffer bytes.Buffer
	var isNullable bool
	timeIndicator := 0
	mastermindStructableAnnotation := ""

	for _, column := range table.Columns {

		colName := strings.Title(column.ColumnName)
		if settings.OutputFormat == "c" {
			colName = CamelCaseString(colName)
		}
		colType, isTime := mapDbColumnTypeToGoType(column.DataType, column.IsNullable)

		if settings.IsMastermindStructable || settings.IsMastermindStructableOnly {

			isPk := ""
			if strings.Contains(column.ColumnDefault.String, "nextval") || // pg
				(strings.Contains(column.ColumnKey, "PRI") && strings.Contains(column.Extra, "auto_increment")) { //mysql
				isPk = `,PRIMARY_KEY,SERIAL,AUTO_INCREMENT`
			}

			mastermindStructableAnnotation = ` stbl:"` + column.ColumnName + isPk + `"`
		}

		if settings.IsMastermindStructableOnly {
			colBuffer.WriteString("\t" + colName + " " + colType + " `" + mastermindStructableAnnotation + "`\n")
		} else {
			colBuffer.WriteString("\t" + colName + " " + colType + " `db:\"" + column.ColumnName + "\"" + mastermindStructableAnnotation + "`\n")
		}

		// collect some info for later use
		if column.IsNullable == "YES" {
			isNullable = true
		}
		if isTime {
			timeIndicator++
		}
	}

	if settings.IsMastermindStructableRecorder && (settings.IsMastermindStructable || settings.IsMastermindStructableOnly) {
		colBuffer.WriteString("\t\nstructable.Recorder\n")
	}

	// create file
	tableName := strings.Title(settings.Prefix + table.TableName + settings.Suffix)
	if settings.OutputFormat == "c" {
		tableName = CamelCaseString(tableName)
	}
	fileName := tableName + ".go"
	outFile, err := os.Create(settings.OutputFilePath + fileName)

	if err != nil {
		return err
	}

	// write head infos
	buffer.WriteString("package " + settings.PackageName + "\n\n")

	// do imports
	if isNullable || timeIndicator > 0 || settings.IsMastermindStructable || settings.IsMastermindStructableOnly {
		buffer.WriteString("import (\n")

		if isNullable {
			buffer.WriteString("\t\"database/sql\"\n")
		}

		if timeIndicator > 0 {
			if isNullable {
				buffer.WriteString("\t\n\"github.com/lib/pq\"\n")
			} else {
				buffer.WriteString("\t\"time\"\n")
			}
		}

		if settings.IsMastermindStructableRecorder && (settings.IsMastermindStructable || settings.IsMastermindStructableOnly) {
			buffer.WriteString("\t\n\"github.com/Masterminds/structable\"\n")
		}

		buffer.WriteString(")\n\n")
	}

	// write struct with fields
	buffer.WriteString("type " + tableName + " struct {\n")
	buffer.WriteString(colBuffer.String())
	buffer.WriteString("}")

	// format it
	formatedFile, _ := format.Source(buffer.Bytes())

	// and save it in file
	outFile.Write(formatedFile)
	outFile.Sync()
	outFile.Close()

	return err
}

func mapDbColumnTypeToGoType(dbDataType string, isNullable string) (goType string, isTime bool) {

	isTime = false

	// first row: postgresql datatypes  // TODO bitstrings, enum, other special types
	// second row: additional mysql datatypes not covered by first row // TODO bit, enums, set
	// and so on

	switch dbDataType {
	case "integer", "bigint", "bigserial", "smallint", "smallserial", "serial",
		"int", "tinyint", "mediumint":
		goType = "int"
		if isNullable == "YES" {
			goType = "sql.NullInt64"
		}
	case "double precision", "numeric", "decimal", "real",
		"float", "double":
		goType = "float64"
		if isNullable == "YES" {
			goType = "sql.NullFloat64"
		}
	case "character varying", "character", "text",
		"char", "varchar", "binary", "varbinary", "blob":
		goType = "string"
		if isNullable == "YES" {
			goType = "sql.NullString"
		}
	case "time", "timestamp", "time with time zone", "timestamp with time zone", "time without time zone", "timestamp without time zone",
		"date", "datetime", "year":
		goType = "time.Time"
		if isNullable == "YES" {
			goType = "pq.NullTime"
		}
		isTime = true
	case "boolean":
		goType = "bool"
		if isNullable == "YES" {
			goType = "sql.NullBool"
		}
	default:
		goType = "sql.NullString"
	}

	return goType, isTime
}