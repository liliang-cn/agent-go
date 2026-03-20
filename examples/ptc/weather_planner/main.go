// Package main demonstrates PTC with a travel planning scenario.
//
// The LLM writes JavaScript that orchestrates multiple tool calls to gather
// weather forecasts, flight prices, and hotel availability across cities,
// then synthesises a recommendation — all in a single JS execution.
//
// Tools:
//   - get_weather_forecast: 5-day forecast for a city
//   - search_flights: one-way flights between cities on a date
//   - search_hotels: hotel availability in a city for given dates
//   - get_city_events: upcoming events/attractions in a city
//
// Usage:
//
//	go run examples/ptc_weather_planner/main.go
//	go run examples/ptc_weather_planner/main.go "Plan a 3-day trip from Beijing to Tokyo next week, budget under $2000"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// ── Domain types ─────────────────────────────────────────────────────────────

// WeatherDay represents a single day's weather forecast.
type WeatherDay struct {
	Date      string  `json:"date"`
	High      float64 `json:"high_celsius"`
	Low       float64 `json:"low_celsius"`
	Condition string  `json:"condition"`
	RainPct   int     `json:"rain_chance_pct"`
}

// Flight represents a flight option.
type Flight struct {
	Airline  string  `json:"airline"`
	FlightNo string  `json:"flight_no"`
	Depart   string  `json:"depart_time"`
	Arrive   string  `json:"arrive_time"`
	PriceUSD float64 `json:"price_usd"`
	Stops    int     `json:"stops"`
}

// Hotel represents a hotel listing.
type Hotel struct {
	Name       string  `json:"name"`
	Stars      int     `json:"stars"`
	PriceNight float64 `json:"price_per_night_usd"`
	Rating     float64 `json:"rating"`
	District   string  `json:"district"`
}

// CityEvent represents an event or attraction.
type CityEvent struct {
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Date     string  `json:"date"`
	PriceUSD float64 `json:"price_usd"`
	Rating   float64 `json:"rating"`
}

// ── Mock data ────────────────────────────────────────────────────────────────

var weatherData = map[string][]WeatherDay{
	"tokyo": {
		{Date: "2026-03-05", High: 14, Low: 6, Condition: "sunny", RainPct: 10},
		{Date: "2026-03-06", High: 12, Low: 5, Condition: "cloudy", RainPct: 30},
		{Date: "2026-03-07", High: 15, Low: 7, Condition: "sunny", RainPct: 5},
		{Date: "2026-03-08", High: 11, Low: 4, Condition: "rainy", RainPct: 80},
		{Date: "2026-03-09", High: 13, Low: 6, Condition: "partly_cloudy", RainPct: 20},
	},
	"osaka": {
		{Date: "2026-03-05", High: 15, Low: 7, Condition: "sunny", RainPct: 5},
		{Date: "2026-03-06", High: 16, Low: 8, Condition: "sunny", RainPct: 10},
		{Date: "2026-03-07", High: 14, Low: 6, Condition: "cloudy", RainPct: 40},
		{Date: "2026-03-08", High: 12, Low: 5, Condition: "rainy", RainPct: 75},
		{Date: "2026-03-09", High: 15, Low: 7, Condition: "sunny", RainPct: 10},
	},
	"bangkok": {
		{Date: "2026-03-05", High: 35, Low: 26, Condition: "sunny", RainPct: 15},
		{Date: "2026-03-06", High: 34, Low: 25, Condition: "partly_cloudy", RainPct: 20},
		{Date: "2026-03-07", High: 33, Low: 26, Condition: "thunderstorm", RainPct: 70},
		{Date: "2026-03-08", High: 34, Low: 25, Condition: "sunny", RainPct: 10},
		{Date: "2026-03-09", High: 36, Low: 27, Condition: "sunny", RainPct: 5},
	},
	"beijing": {
		{Date: "2026-03-05", High: 8, Low: -2, Condition: "sunny", RainPct: 5},
		{Date: "2026-03-06", High: 10, Low: 0, Condition: "windy", RainPct: 10},
		{Date: "2026-03-07", High: 7, Low: -3, Condition: "cloudy", RainPct: 25},
		{Date: "2026-03-08", High: 9, Low: -1, Condition: "sunny", RainPct: 5},
		{Date: "2026-03-09", High: 11, Low: 1, Condition: "sunny", RainPct: 10},
	},
}

var flightData = map[string][]Flight{
	"beijing-tokyo": {
		{Airline: "Air China", FlightNo: "CA925", Depart: "08:30", Arrive: "12:45", PriceUSD: 420, Stops: 0},
		{Airline: "ANA", FlightNo: "NH964", Depart: "14:00", Arrive: "18:20", PriceUSD: 380, Stops: 0},
		{Airline: "Spring Airlines", FlightNo: "9C8952", Depart: "06:15", Arrive: "10:30", PriceUSD: 195, Stops: 0},
		{Airline: "China Eastern", FlightNo: "MU535", Depart: "10:00", Arrive: "16:30", PriceUSD: 290, Stops: 1},
	},
	"beijing-osaka": {
		{Airline: "Air China", FlightNo: "CA927", Depart: "09:00", Arrive: "13:00", PriceUSD: 390, Stops: 0},
		{Airline: "Peach Aviation", FlightNo: "MM090", Depart: "15:30", Arrive: "19:40", PriceUSD: 210, Stops: 0},
		{Airline: "China Southern", FlightNo: "CZ611", Depart: "07:45", Arrive: "14:10", PriceUSD: 270, Stops: 1},
	},
	"beijing-bangkok": {
		{Airline: "Air China", FlightNo: "CA979", Depart: "08:00", Arrive: "12:30", PriceUSD: 350, Stops: 0},
		{Airline: "Thai Airways", FlightNo: "TG615", Depart: "23:45", Arrive: "04:10", PriceUSD: 310, Stops: 0},
		{Airline: "China Southern", FlightNo: "CZ357", Depart: "11:00", Arrive: "17:30", PriceUSD: 240, Stops: 1},
	},
	"tokyo-beijing": {
		{Airline: "ANA", FlightNo: "NH963", Depart: "09:00", Arrive: "12:15", PriceUSD: 395, Stops: 0},
		{Airline: "Air China", FlightNo: "CA926", Depart: "14:30", Arrive: "17:45", PriceUSD: 410, Stops: 0},
		{Airline: "Spring Airlines", FlightNo: "9C8951", Depart: "18:00", Arrive: "21:10", PriceUSD: 185, Stops: 0},
	},
}

var hotelData = map[string][]Hotel{
	"tokyo": {
		{Name: "Shinjuku Granbell Hotel", Stars: 3, PriceNight: 85, Rating: 4.2, District: "Shinjuku"},
		{Name: "Hotel Gracery Shinjuku", Stars: 4, PriceNight: 130, Rating: 4.5, District: "Shinjuku"},
		{Name: "Park Hyatt Tokyo", Stars: 5, PriceNight: 450, Rating: 4.8, District: "Nishi-Shinjuku"},
		{Name: "Tokyu Stay Ikebukuro", Stars: 3, PriceNight: 70, Rating: 4.0, District: "Ikebukuro"},
		{Name: "The Gate Hotel Asakusa", Stars: 4, PriceNight: 150, Rating: 4.6, District: "Asakusa"},
	},
	"osaka": {
		{Name: "Cross Hotel Osaka", Stars: 4, PriceNight: 95, Rating: 4.4, District: "Shinsaibashi"},
		{Name: "Hotel Granvia Osaka", Stars: 4, PriceNight: 120, Rating: 4.3, District: "Namba"},
		{Name: "Namba Oriental Hotel", Stars: 3, PriceNight: 65, Rating: 4.1, District: "Namba"},
		{Name: "Conrad Osaka", Stars: 5, PriceNight: 380, Rating: 4.7, District: "Nakanoshima"},
	},
	"bangkok": {
		{Name: "Ibis Styles Sukhumvit", Stars: 3, PriceNight: 35, Rating: 4.0, District: "Sukhumvit"},
		{Name: "Grande Centre Point Terminal 21", Stars: 4, PriceNight: 75, Rating: 4.5, District: "Sukhumvit"},
		{Name: "Mandarin Oriental", Stars: 5, PriceNight: 320, Rating: 4.9, District: "Riverside"},
		{Name: "Siam@Siam Design Hotel", Stars: 4, PriceNight: 60, Rating: 4.3, District: "Siam"},
	},
}

var eventsData = map[string][]CityEvent{
	"tokyo": {
		{Name: "teamLab Borderless", Category: "art", Date: "daily", PriceUSD: 25, Rating: 4.7},
		{Name: "Tsukiji Outer Market Tour", Category: "food", Date: "daily", PriceUSD: 40, Rating: 4.6},
		{Name: "Cherry Blossom Preview Festival", Category: "festival", Date: "2026-03-08", PriceUSD: 0, Rating: 4.5},
		{Name: "Sumo Tournament", Category: "sport", Date: "2026-03-07", PriceUSD: 60, Rating: 4.8},
	},
	"osaka": {
		{Name: "Dotonbori Street Food Walk", Category: "food", Date: "daily", PriceUSD: 35, Rating: 4.7},
		{Name: "Osaka Castle Night Illumination", Category: "culture", Date: "2026-03-06", PriceUSD: 10, Rating: 4.4},
		{Name: "Universal Studios Japan", Category: "theme_park", Date: "daily", PriceUSD: 70, Rating: 4.6},
	},
	"bangkok": {
		{Name: "Grand Palace & Wat Phra Kaew", Category: "culture", Date: "daily", PriceUSD: 15, Rating: 4.7},
		{Name: "Chatuchak Weekend Market", Category: "shopping", Date: "2026-03-07", PriceUSD: 0, Rating: 4.5},
		{Name: "Muay Thai Live Show", Category: "entertainment", Date: "2026-03-06", PriceUSD: 30, Rating: 4.3},
		{Name: "Floating Market Tour", Category: "culture", Date: "daily", PriceUSD: 25, Rating: 4.4},
	},
}

// ── Tool handlers ────────────────────────────────────────────────────────────

func handleGetWeather(_ context.Context, args map[string]interface{}) (interface{}, error) {
	city, _ := args["city"].(string)
	if city == "" {
		return nil, fmt.Errorf("city is required")
	}
	city = strings.ToLower(city)

	forecast, ok := weatherData[city]
	if !ok {
		return nil, fmt.Errorf("no weather data for city: %s", city)
	}

	return map[string]interface{}{
		"city":     city,
		"forecast": forecast,
		"days":     len(forecast),
	}, nil
}

func handleSearchFlights(_ context.Context, args map[string]interface{}) (interface{}, error) {
	from, _ := args["from"].(string)
	to, _ := args["to"].(string)

	if from == "" || to == "" {
		return nil, fmt.Errorf("from and to are required")
	}
	route := strings.ToLower(from) + "-" + strings.ToLower(to)

	flights, ok := flightData[route]
	if !ok {
		return map[string]interface{}{
			"route":   route,
			"flights": []Flight{},
			"count":   0,
			"message": "no flights found for this route",
		}, nil
	}

	// Apply optional max_price filter.
	maxPrice, hasMax := args["max_price_usd"].(float64)
	if !hasMax {
		// Try integer form from JSON.
		if v, ok := args["max_price_usd"].(json.Number); ok {
			if f, err := v.Float64(); err == nil {
				maxPrice = f
				hasMax = true
			}
		}
	}

	var filtered []Flight
	for _, f := range flights {
		if hasMax && f.PriceUSD > maxPrice {
			continue
		}
		filtered = append(filtered, f)
	}

	return map[string]interface{}{
		"route":   route,
		"flights": filtered,
		"count":   len(filtered),
	}, nil
}

func handleSearchHotels(_ context.Context, args map[string]interface{}) (interface{}, error) {
	city, _ := args["city"].(string)
	if city == "" {
		return nil, fmt.Errorf("city is required")
	}
	city = strings.ToLower(city)

	hotels, ok := hotelData[city]
	if !ok {
		return nil, fmt.Errorf("no hotel data for city: %s", city)
	}

	// Apply optional max_price and min_stars filters.
	maxPrice := math.MaxFloat64
	if v, ok := args["max_price_per_night"].(float64); ok {
		maxPrice = v
	}
	minStars := 0
	if v, ok := args["min_stars"].(float64); ok {
		minStars = int(v)
	}

	var filtered []Hotel
	for _, h := range hotels {
		if h.PriceNight > maxPrice {
			continue
		}
		if h.Stars < minStars {
			continue
		}
		filtered = append(filtered, h)
	}

	return map[string]interface{}{
		"city":   city,
		"hotels": filtered,
		"count":  len(filtered),
	}, nil
}

func handleGetCityEvents(_ context.Context, args map[string]interface{}) (interface{}, error) {
	city, _ := args["city"].(string)
	if city == "" {
		return nil, fmt.Errorf("city is required")
	}
	city = strings.ToLower(city)

	events, ok := eventsData[city]
	if !ok {
		return nil, fmt.Errorf("no events data for city: %s", city)
	}

	// Apply optional category filter.
	cat, _ := args["category"].(string)
	if cat == "" {
		return map[string]interface{}{
			"city":   city,
			"events": events,
			"count":  len(events),
		}, nil
	}

	cat = strings.ToLower(cat)
	var filtered []CityEvent
	for _, e := range events {
		if strings.ToLower(e.Category) == cat {
			filtered = append(filtered, e)
		}
	}

	return map[string]interface{}{
		"city":     city,
		"category": cat,
		"events":   filtered,
		"count":    len(filtered),
	}, nil
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	question := "I want to plan a 3-day trip from Beijing next week. " +
		"Compare Tokyo, Osaka, and Bangkok — check weather, cheapest direct flights, " +
		"hotels under $150/night, and what events are happening. " +
		"Recommend the best destination and give me a rough budget breakdown."
	if len(os.Args) > 1 {
		question = strings.Join(os.Args[1:], " ")
	}

	fmt.Println("=== PTC Weather & Travel Planner ===")
	fmt.Printf("Question: %s\n\n", question)

	svc, err := createService()
	if err != nil {
		log.Fatalf("Failed to create agent service: %v", err)
	}
	defer svc.Close()

	// Show agent info
	info := svc.Info()
	fmt.Printf("--- Agent Configuration ---\n")
	fmt.Printf("  Model:    %s\n", info.Model)
	fmt.Printf("  Base URL: %s\n", info.BaseURL)
	fmt.Printf("  PTC:      %v\n", info.PTCEnabled)
	fmt.Println("---------------------------")
	fmt.Println()

	registerTools(svc)

	if err := runWithStream(ctx, svc, question); err != nil {
		log.Fatalf("Execution failed: %v", err)
	}
}

func createService() (*agent.Service, error) {
	return agent.New("TravelPlanner").
		WithPTC().
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
}

func registerTools(svc *agent.Service) {
	// Struct-based (typed params) — auto-generated JSON Schema.

	type weatherForecastParams struct {
		City string `json:"city" desc:"City name (e.g. tokyo, osaka, bangkok, beijing)" required:"true"`
	}
	svc.Register(agent.NewTool(
		"get_weather_forecast",
		"Get a 5-day weather forecast for a city. Returns { city, forecast: [{ date, high_celsius, low_celsius, condition, rain_chance_pct }], days }.",
		func(ctx context.Context, p *weatherForecastParams) (any, error) {
			return handleGetWeather(ctx, map[string]interface{}{"city": p.City})
		},
	))

	type searchFlightsParams struct {
		From        string   `json:"from"          desc:"Departure city" required:"true"`
		To          string   `json:"to"            desc:"Arrival city" required:"true"`
		MaxPriceUSD *float64 `json:"max_price_usd" desc:"Maximum price filter in USD (optional)"`
	}
	svc.Register(agent.NewTool(
		"search_flights",
		"Search for one-way flights between two cities. Returns { route, flights: [{ airline, flight_no, depart_time, arrive_time, price_usd, stops }], count }.",
		func(ctx context.Context, p *searchFlightsParams) (any, error) {
			args := map[string]interface{}{
				"from": p.From,
				"to":   p.To,
			}
			if p.MaxPriceUSD != nil {
				args["max_price_usd"] = *p.MaxPriceUSD
			}
			return handleSearchFlights(ctx, args)
		},
	))

	// Builder-based (fluent) — equivalent expressive power.
	svc.Register(
		agent.BuildTool("search_hotels").
			Description("Search for hotels in a city with optional filters. Returns { city, hotels: [{ name, stars, price_per_night_usd, rating, district }], count }.").
			Param("city", agent.TypeString, "City name", agent.Required()).
			Param("max_price_per_night", agent.TypeNumber, "Maximum price per night in USD (optional)").
			Param("min_stars", agent.TypeNumber, "Minimum star rating (optional, 1-5)").
			Handler(handleSearchHotels).
			Build(),
	)

	svc.Register(
		agent.BuildTool("get_city_events").
			Description("Get upcoming events and attractions in a city. Returns { city, events: [{ name, category, date, price_usd, rating }], count }.").
			Param("city", agent.TypeString, "City name", agent.Required()).
			Param("category", agent.TypeString, "Filter by category: food, culture, art, sport, entertainment, shopping, theme_park (optional)").
			Handler(handleGetCityEvents).
			Build(),
	)
}

func runWithStream(ctx context.Context, svc *agent.Service, question string) error {
	events, err := svc.RunStream(ctx, question)
	if err != nil {
		return fmt.Errorf("RunStream: %w", err)
	}

	for evt := range events {
		switch evt.Type {
		case agent.EventTypeStart:
			fmt.Printf("[start] %s\n", evt.Content)
		case agent.EventTypeThinking:
			content := evt.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			fmt.Printf("[thinking] %s\n", content)
		case agent.EventTypeToolCall:
			argsJSON, _ := json.MarshalIndent(evt.ToolArgs, "  ", "  ")
			fmt.Printf("[tool_call] %s(%s)\n", evt.ToolName, string(argsJSON))
		case agent.EventTypeToolResult:
			result := fmt.Sprintf("%v", evt.ToolResult)
			if len(result) > 500 {
				result = result[:500] + "..."
			}
			fmt.Printf("[tool_result] %s -> %s\n", evt.ToolName, result)
		case agent.EventTypePartial:
			fmt.Print(evt.Content)
		case agent.EventTypeComplete:
			if evt.Content != "" {
				fmt.Printf("\n\n=== Final Answer ===\n%s\n", evt.Content)
			}
		case agent.EventTypeError:
			fmt.Printf("[error] %s\n", evt.Content)
			return fmt.Errorf("agent error: %s", evt.Content)
		case agent.EventTypeHandoff:
			fmt.Printf("[handoff] %s\n", evt.Content)
		}
	}

	return nil
}
