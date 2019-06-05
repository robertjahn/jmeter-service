package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/keptn/go-utils/pkg/utils"
)

func executeJMeter(scriptName string, resultsDir string, serverURL string, serverPort int, checkPath string, vuCount int,
	loopCount int, thinkTime int, LTN string, funcValidation bool, avgRtValidation int) (bool, error) {

	os.RemoveAll(resultsDir)
	os.MkdirAll(resultsDir, 0644)

	res, err := utils.ExecuteCommand("jmeter", []string{"-n", "-t", "./" + scriptName,
		// "-e", "-o", resultsDir,
		"-l", resultsDir + "_result.tlf",
		"-JSERVER_URL=" + serverURL,
		"-JDT_LTN=" + LTN,
		"-JVUCount=" + strconv.Itoa(vuCount),
		"-JLoopCount=" + strconv.Itoa(loopCount),
		"-JCHECK_PATH=" + checkPath,
		"-JSERVER_PORT=" + strconv.Itoa(serverPort),
		"-JThinkTime=" + strconv.Itoa(thinkTime)})

	fmt.Println(res)
	if err != nil {
		fmt.Println(err)
		return false, err
	}

	// Parse result
	summary := getLastOccurence(strings.Split(res, "\n"), "summary =")
	if summary == "" {
		return false, errors.New("Cannot parse jmeter-result")
	}

	space := regexp.MustCompile(`\s+`)
	splits := strings.Split(space.ReplaceAllString(summary, " "), " ")
	errorCount, err := strconv.Atoi(splits[14])
	if err != nil {
		return false, errors.New("Cannot parse jmeter-result")
	}

	if funcValidation && errorCount > 0 {
		return false, nil
	}

	avg, err := strconv.Atoi(splits[8])
	if err != nil {
		return false, errors.New("Cannot parse jmeter-result")
	}

	if avgRtValidation > 0 && avg > avgRtValidation {
		return false, nil
	}

	return true, nil
}

func getLastOccurence(vs []string, prefix string) string {
	for i := len(vs) - 1; i >= 0; i-- {
		if strings.HasPrefix(vs[i], prefix) {
			return vs[i]
		}
	}
	return ""
}
