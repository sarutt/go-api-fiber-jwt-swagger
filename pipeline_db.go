package main

import (
	"log"
	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// db is the pipeline datastore. The books/upload/config handlers that shipped
// with this boilerplate still use their own in-memory storage.
var db *gorm.DB

// initDB opens the pipeline database, creates the tables and seeds the show
// bible on first run. DB_PATH overrides the default file location.
func initDB() {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "pompomhollow.db"
	}

	if err := setupDB(path); err != nil {
		log.Fatalf("cannot open database %s: %v", path, err)
	}
	log.Printf("pipeline database ready at %s", path)
}

// setupDB connects, migrates and seeds. Split out from initDB so tests can
// point it at an in-memory database without taking the process down on error.
func setupDB(path string) error {
	conn, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return err
	}
	db = conn

	// SQLite takes one writer at a time. Capping the pool at a single
	// connection makes concurrent workers queue instead of failing on a
	// locked database, which matters as soon as more than one agent runs.
	// Moving to Postgres is what lifts this limit.
	sqlDB, err := conn.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(
		&Episode{},
		&Character{},
		&Location{},
		&CurriculumTopic{},
		&Asset{},
		&ApprovalLog{},
		&EpisodeEvent{},
		&PipelineControl{},
		&Alert{},
	); err != nil {
		return err
	}

	seedShowBible()
	return nil
}

// seedShowBible loads the fixed cast, locations and first curriculum cycle.
// It only writes tables that are still empty, so restarts never duplicate
// rows and never overwrite edits made through the API.
func seedShowBible() {
	var characterCount int64
	db.Model(&Character{}).Count(&characterCount)
	if characterCount == 0 {
		cast := []Character{
			{Name: "Pommy", Species: "fox", Role: "lead", ColorHex: "#ff7a47", SignatureItem: "blue scarf", Teaches: "courage & adventure", VoiceProfileID: "voice-pommy"},
			{Name: "Bibi", Species: "rabbit", Role: "best friend", ColorHex: "#e8779f", SignatureItem: "round glasses", Teaches: "counting 1-10", VoiceProfileID: "voice-bibi"},
			{Name: "Zizi", Species: "owl", Role: "mentor", ColorHex: "#17847d", SignatureItem: "treetop nest", Teaches: "letters & ABC", VoiceProfileID: "voice-zizi"},
			{Name: "Glow", Species: "firefly", Role: "helper", ColorHex: "#c98a10", SignatureItem: "glowing light", Teaches: "colors & night courage", VoiceProfileID: "voice-glow"},
			{Name: "Shelly", Species: "turtle", Role: "the calm one", ColorHex: "#3f9142", SignatureItem: "patterned shell", Teaches: "emotions & patience", VoiceProfileID: "voice-shelly"},
			{Name: "Momma Pom", Species: "fox", Role: "parent", ColorHex: "#8b6a4f", SignatureItem: "apron", Teaches: "safety & life lessons", VoiceProfileID: "voice-momma"},
			{Name: "Papa Pom", Species: "fox", Role: "parent", ColorHex: "#6f5540", SignatureItem: "woven hat", Teaches: "safety & life lessons", VoiceProfileID: "voice-papa"},
		}
		if err := db.Create(&cast).Error; err != nil {
			log.Printf("seed cast failed: %v", err)
		}
	}

	var locationCount int64
	db.Model(&Location{}).Count(&locationCount)
	if locationCount == 0 {
		sets := []Location{
			{Name: "The Great Pom Tree", Description: "Home base inside a hollow rainbow tree.", UsedFor: "every episode opens and closes here"},
			{Name: "Giggle Meadow", Description: "A wide flower field.", UsedFor: "counting episodes"},
			{Name: "Letter Grove", Description: "Bamboo grown in the shape of letters.", UsedFor: "alphabet episodes"},
			{Name: "Star Pond", Description: "A still pond reflecting the stars.", UsedFor: "night-time and feelings episodes"},
			{Name: "Rainbow Bridge", Description: "An arching rainbow between the sets.", UsedFor: "the recurring transition between acts"},
		}
		if err := db.Create(&sets).Error; err != nil {
			log.Printf("seed locations failed: %v", err)
		}
	}

	var topicCount int64
	db.Model(&CurriculumTopic{}).Count(&topicCount)
	if topicCount == 0 {
		topics := []CurriculumTopic{
			{Theme: ThemeCounting, Title: "Counting to five", LearningGoal: "Count objects up to five out loud.", WeekInCycle: 1, DifficultyLevel: 1},
			{Theme: ThemeLetters, Title: "The letter A", LearningGoal: "Recognise the letter A and its sound.", WeekInCycle: 2, DifficultyLevel: 1},
			{Theme: ThemeEmotions, Title: "Feeling shy", LearningGoal: "Name the feeling of shyness and one way to handle it.", WeekInCycle: 3, DifficultyLevel: 1},
			{Theme: ThemeKindness, Title: "Sharing a snack", LearningGoal: "Understand sharing as an act of kindness.", WeekInCycle: 4, DifficultyLevel: 1},
			{Theme: ThemeCounting, Title: "Counting to ten", LearningGoal: "Count objects up to ten out loud.", WeekInCycle: 1, DifficultyLevel: 2},
			{Theme: ThemeLetters, Title: "The letter B", LearningGoal: "Recognise the letter B and its sound.", WeekInCycle: 2, DifficultyLevel: 2},
			{Theme: ThemeEmotions, Title: "Feeling frustrated", LearningGoal: "Name frustration and try again calmly.", WeekInCycle: 3, DifficultyLevel: 2},
			{Theme: ThemeKindness, Title: "Helping a friend", LearningGoal: "Notice when a friend needs help and offer it.", WeekInCycle: 4, DifficultyLevel: 2},
		}
		if err := db.Create(&topics).Error; err != nil {
			log.Printf("seed curriculum failed: %v", err)
		}
	}
}
