package orchestrate

import (
	"context"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/adrg/frontmatter"
)

type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusDone
	StatusFailed
)

// Description of the plan, reason for the plan, and potential solution.
type ExecutionPlanner struct {
	id              string
	TaskDescription string `json:"task_description" yaml:"description"`
	Reason          string `json:"reason" yaml:"reason"`
	Solution        string `json:"solution" yaml:"solution"`
	Version         uint32 `json:"version" yaml:"version"`
}

type ExecutionResult struct {
	Success    bool         `json:"success" yaml:"success"`
	Message    string       `json:"message" yaml:"message"`
	Handoff    bool         `json:"handoff" yaml:"handoff"`
	SharedData []SharedData `json:"shared_data,omitempty" yaml:"shared_data,omitempty"`
}

type SharedData struct {
	// Define the structure of the shared data that can be passed between tasks.
	FilePath    string `json:"file_path" yaml:"file_path"`
	Description string `json:"description" yaml:"description"`
}

type Metadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	License     string `yaml:"license"`
}

type Executor struct {
	// Add fields for managing state, configuration, etc.
	logger *log.Logger

	// The workspace field is used to specify the directory path of the skill, which contains the SKILL.md file and other necessary files for the execution of tasks.
	workspace string
	planner   *ExecutionPlanner
	Result    *ExecutionResult
	Status    Status

	// Added for mockability during engine execution. Temporary placeholder to pass current tests
	RunE func(ctx context.Context, parentOutputs map[string]any) (output any, newNodes []*Node, err error)

	// Metadata about the skill, such as name, description, license, etc.
	Metadata
}

func NewExecutor(logger *log.Logger, workspace string, planner *ExecutionPlanner) (*Executor, error) {
	u, err := url.Parse(workspace)
	_ = u
	// Check if the skill is a valid directory path and contains SKILL.md
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		logger.Fatalf("skill is not a valid directory path: %s", workspace)
		return nil, err
	}
	if _, err := os.Stat(workspace + string(os.PathSeparator) + "SKILL.md"); os.IsNotExist(err) {
		logger.Fatalf("SKILL.md not found in skill directory: %s", workspace)
		return nil, err
	}
	skillPath := workspace + string(os.PathSeparator) + "SKILL.md"
	content, err := os.ReadFile(skillPath)
	if err != nil {
		logger.Fatalf("failed to read SKILL.md: %v", err)
		return nil, err
	}

	metadata := Metadata{}
	_, err = frontmatter.Parse(strings.NewReader(string(content)), &metadata)
	if err != nil {
		logger.Fatalf("failed to parse SKILL.md front matter: %v", err)
		return nil, err
	}

	logger.Println("Creating a new Executor...")
	return &Executor{
		logger:    logger,
		workspace: workspace,
		planner:   planner,
		Metadata:  metadata,
	}, nil
}

func (e *Executor) Plan() error {
	// Implement the logic to plan the execution of tasks based on the request, manage state, etc.
	e.logger.Println("Planning tasks...")

	if err := e.planTask(); err != nil {
		e.logger.Printf("Error planning tasks: %v", err)
		return err
	}

	return nil
}

func (e *Executor) planTask() error {
	// Implement the logic to plan a task based on the execution plan, manage state, etc.
	e.logger.Println("Planning a task...")

	e.planner.Reason = "The task requires processing data and generating a report."
	e.planner.Solution = "Use a data processing library to analyze the data and generate the report."

	e.planner.Version += 1

	return nil
}

func (e *Executor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	// Implement the logic to execute tasks based on the request, manage state, handle results, etc.
	if e.logger != nil {
		e.logger.Println("Executing tasks...")
	}

	// Temporary bridge to preserve compatibility with existing dynamic closures until Executor is fully refactored.
	if e.RunE != nil {
		return e.RunE(ctx, parentOutputs)
	}

	return nil, nil, nil
}
