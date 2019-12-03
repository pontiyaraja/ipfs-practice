package metro

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var directionMap map[string]string

const (
	routeURL = "http://svc.metrotransit.org/NexTrip/Routes"
	//http://svc.metrotransit.org/NexTrip/Directions/{ROUTE}
	directionURL = "http://svc.metrotransit.org/NexTrip/Directions/"
	//http://svc.metrotransit.org/NexTrip/Stops/{ROUTE}/{DIRECTION}
	stopsURL = "http://svc.metrotransit.org/NexTrip/Stops/"
	//http://svc.metrotransit.org/NexTrip/{ROUTE}/{DIRECTION}/{STOP}
	timeDeparturesURL = "http://svc.metrotransit.org/NexTrip/"
)

var noBusError error

func init() {
	directionMap = make(map[string]string)
	//1 = South, 2 = East, 3 = West, 4 = North.
	directionMap["south"] = "1"
	directionMap["east"] = "2"
	directionMap["west"] = "3"
	directionMap["north"] = "4"

	noBusError = errors.New("no bus found")
}

func doRequest(URL, method string) ([]byte, error) {
	req, err := http.NewRequest(method, URL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("got wrong status from server")
	}
	respByte, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return respByte, err
}

func GetETATime(route, stop, direction string) {
	routeID, err := getRoute(route)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	directionData, err := getDirection(routeID, direction)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	stopID, err := getStops(routeID, directionData, stop)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	etaTime, err := getTimeDepartures(routeID, directionData, stopID)
	if err == nil {
		fmt.Println(etaTime, "Minutes")
	}
	if err != nil && strings.Compare(noBusError.Error(), err.Error()) != 0 {
		fmt.Println(err)
	}
}

func getRoute(busRoute string) (string, error) {
	fmt.Println(busRoute)
	resp, err := doRequest(routeURL, http.MethodGet)
	if err != nil {
		fmt.Println(err)
		return "", err
	}
	var routeList []Routes
	err = json.NewDecoder(bytes.NewReader(resp)).Decode(&routeList)
	if err != nil {
		fmt.Println(err)
	}
	for _, rout := range routeList {
		data := strings.Split(rout.Description, "-")
		if len(data) > 1 && strings.Compare(strings.TrimSuffix(data[0], " "), rout.Route) == 0 {
			if strings.Compare(rout.Route+" - "+busRoute, rout.Description) == 0 {
				return rout.Route, nil
			}
		}
		if strings.Compare(busRoute, rout.Description) == 0 {
			return rout.Route, nil
		}
	}
	return "", errors.New("no matching routes")
}

func getDirection(route, direction string) (string, error) {
	resp, err := doRequest(directionURL+route, http.MethodGet)
	if err != nil {
		fmt.Println(err)
		return "", err
	}
	var directionList []Direction
	err = json.NewDecoder(bytes.NewReader(resp)).Decode(&directionList)
	if err != nil {
		fmt.Println(err)
		return "", err
	}
	for _, directionData := range directionList {
		fmt.Println(directionData.Value, directionMap[strings.ToLower(direction)])
		if strings.Compare(directionData.Value, directionMap[strings.ToLower(direction)]) == 0 {
			return directionData.Value, nil
		}
	}
	return "", errors.New("no directions found")
}

func getStops(route, direction, busStop string) (string, error) {
	resp, err := doRequest(stopsURL+route+"/"+direction, http.MethodGet)
	if err != nil {
		fmt.Println(err)
		return "", err
	}
	var stopList []Stops
	err = json.NewDecoder(bytes.NewReader(resp)).Decode(&stopList)
	if err != nil {
		fmt.Println(err)
		return "", err
	}
	for _, stop := range stopList {
		if strings.Compare(strings.ToLower(busStop), strings.ToLower(stop.Text)) == 0 {
			return stop.Value, nil
		}
	}
	return "", errors.New("no stop found")
}

func getTimeDepartures(route, direction, stop string) (float64, error) {
	fmt.Println(route, direction, stop)
	resp, err := doRequest(timeDeparturesURL+route+"/"+direction+"/"+stop, http.MethodGet)
	if err != nil {
		fmt.Println(err)
		return 0, err
	}
	var timeDepList []TimeDepartures
	err = json.NewDecoder(bytes.NewReader(resp)).Decode(&timeDepList)
	if err != nil {
		fmt.Println(err)
		return 0, err
	}
	for _, timeDep := range timeDepList {
		stringDate := strings.TrimPrefix(timeDep.DepartureTime, "/Date(")
		stringDate = strings.TrimSuffix(stringDate, ")/")
		stringDate = strings.TrimSuffix(stringDate, "-0500")
		intDate, err := strconv.Atoi(stringDate)
		if err != nil {
			fmt.Println(err)
			return 0, err
		}
		unixTimeUTC := time.Unix(int64(intDate)/1000, 0)
		timeNow := time.Now()
		subTime := unixTimeUTC.Sub(timeNow).Minutes()
		if subTime > 0 {
			return math.Floor(subTime), nil
		}
	}
	return 0, noBusError
}
