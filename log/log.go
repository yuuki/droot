package log

import (
	"log"
)

var IsDebug = false

func init() {
	log.SetFlags(0)
}

func Debug(v ...interface{}) {
	if IsDebug == true {
		log.Println(v...)
	}
}

func Debugf(format string, v ...interface{}) {
	if IsDebug == true {
		log.Printf(format, v...)
	}
}

func Info(v ...interface{}) {
	log.Println(v...)
}

func Infof(format string, v ...interface{}) {
	log.Printf(format, v...)
}

func Error(v ...interface{}) {
	log.Fatal(v...)
}

func Errorf(format string, v ...interface{}) {
	log.Fatalf(format, v...)
}
