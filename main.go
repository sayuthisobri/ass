package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	ssoTypes "github.com/aws/aws-sdk-go-v2/service/sso/types"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFDF5")).
			Background(lipgloss.Color("#5D4A7D")).
			Padding(0, 1).
			Bold(true)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFDF5")).
			Background(lipgloss.Color("#7D5BA6")).
			Padding(0, 1)

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0")).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262")).
			Padding(0, 2)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF5555")).
			Bold(true)

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#50FA7B")).
			Bold(true)
)

type AccountItem struct {
	AccountID   string
	AccountName string
	Email       string
}

type RoleItem struct {
	RoleName string
}

type Profile struct {
	Name         string
	SSOAccountID string
	SSOSession   string
	SSORoleName  string
	Region       string
	OutputFormat string
}

func (i RoleItem) FilterValue() string {
	return i.RoleName
}

func (i AccountItem) FilterValue() string {
	return fmt.Sprintf("%s %s %s", i.AccountName, i.AccountID, i.Email)
}

type RoleDelegate struct{}

func NewRoleDelegate() *RoleDelegate {
	return &RoleDelegate{}
}

func (d RoleDelegate) Height() int                             { return 1 }
func (d RoleDelegate) Spacing() int                            { return 0 }
func (d RoleDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d RoleDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	item, ok := listItem.(RoleItem)
	if !ok {
		return
	}

	roleName := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFDF5")).
		Width(50).
		Render(item.RoleName)

	styleFn := normalStyle
	if index == m.Index() {
		styleFn = selectedStyle
	}

	fmt.Fprint(w, styleFn.Render(roleName))
}

type AccountDelegate struct{}

func NewAccountDelegate() *AccountDelegate {
	return &AccountDelegate{}
}

func (d AccountDelegate) Height() int                             { return 1 }
func (d AccountDelegate) Spacing() int                            { return 0 }
func (d AccountDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d AccountDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	item, ok := listItem.(AccountItem)
	if !ok {
		return
	}

	nameWidth := 35
	emailWidth := 45

	name := item.AccountName
	if len(name) > nameWidth {
		name = name[:nameWidth-3] + "..."
	}

	email := item.Email
	if len(email) > emailWidth {
		email = email[:emailWidth-3] + "..."
	}

	row := lipgloss.NewStyle().
		Width(nameWidth).
		Foreground(lipgloss.Color("#FFFDF5")).
		Render(name) +
		lipgloss.NewStyle().
			Width(20).
			Foreground(lipgloss.Color("#50FA7B")).
			Render(item.AccountID) +
		lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0")).
			Render(email)

	styleFn := normalStyle
	if index == m.Index() {
		styleFn = selectedStyle
	}

	fmt.Fprint(w, styleFn.Render(row))
}

type state int

const (
	stateLoading state = iota
	stateSelecting
	stateFetchingRoles
	stateSelectingRole
	stateConfiguring
	stateDone
	stateError
)

type model struct {
	list            list.Model
	roleList        list.Model
	state           state
	accounts        []AccountItem
	roles           []ssoTypes.RoleInfo
	ssoSession      string
	region          string
	ssoRegion       string
	accessToken     string
	err             error
	statusMessage   string
	width           int
	height          int
	writeCreds      bool
	outputFormat    string
	dryRun          bool
	selectedAccount AccountItem
	selectedRole    RoleItem
	profileName     string
	outputMessage   string // For displaying success output in View()
	namePrefix      string
	nameSuffix      string
}

func initialModel(ssoSession, region, ssoRegion, outputFormat string, writeCreds, dryRun bool, namePrefix, nameSuffix string) model {
	l := list.New([]list.Item{}, NewAccountDelegate(), 0, 0)
	l.Title = "Loading AWS SSO Accounts..."
	l.Styles.Title = titleStyle
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.FilterInput.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFDF5"))
	l.SetShowPagination(false)
	l.SetShowFilter(false)
	l.SetShowTitle(false)
	l.SetShowHelp(false)

	// Remove list margins/padding and border
	l.Styles.NoItems = lipgloss.NewStyle()
	l.Styles.ActivePaginationDot = lipgloss.NewStyle()
	l.Styles.InactivePaginationDot = lipgloss.NewStyle()

	rl := list.New([]list.Item{}, NewRoleDelegate(), 0, 0)
	rl.Styles.Title = titleStyle
	rl.SetShowStatusBar(false)
	rl.SetFilteringEnabled(false)
	rl.SetShowPagination(false)
	rl.SetShowFilter(false)
	rl.SetShowTitle(false)
	rl.SetShowHelp(false)
	rl.Styles.NoItems = lipgloss.NewStyle()
	rl.Styles.ActivePaginationDot = lipgloss.NewStyle()
	rl.Styles.InactivePaginationDot = lipgloss.NewStyle()

	return model{
		list:         l,
		roleList:     rl,
		state:        stateLoading,
		ssoSession:   ssoSession,
		region:       region,
		ssoRegion:    ssoRegion,
		width:        80,
		height:       24,
		writeCreds:   writeCreds,
		dryRun:       dryRun,
		outputFormat: outputFormat,
		namePrefix:   namePrefix,
		nameSuffix:   nameSuffix,
	}
}

func getSSOTokenFromCache(ssoSession string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")
	files, err := filepath.Glob(filepath.Join(cacheDir, "*.json"))
	if err != nil {
		return "", fmt.Errorf("failed to list cache files: %w", err)
	}

	if len(files) == 0 {
		return "", fmt.Errorf("no SSO cache files found. Run 'aws sso login' first")
	}

	// Find the most recently modified file for the specific SSO session
	var latestFile string
	var latestTime time.Time

	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		if latestFile == "" || info.ModTime().After(latestTime) {
			latestFile = file
			latestTime = info.ModTime()
		}
	}

	if latestFile == "" {
		return "", fmt.Errorf("no valid SSO cache files found")
	}

	data, err := os.ReadFile(latestFile)
	if err != nil {
		return "", fmt.Errorf("failed to read cache file: %w", err)
	}

	var cacheData struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   string `json:"expiresAt"`
	}
	if err := json.Unmarshal(data, &cacheData); err != nil {
		return "", fmt.Errorf("failed to parse cache file: %w", err)
	}

	if cacheData.AccessToken == "" {
		return "", fmt.Errorf("no access token found in cache")
	}

	// Check if token is expired
	if cacheData.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, cacheData.ExpiresAt)
		if err == nil && time.Now().After(expiresAt) {
			return "", fmt.Errorf("access token has expired. Browser authentication required.", ssoSession)
		}
	}

	return cacheData.AccessToken, nil
}

func isExpiredTokenError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "expired token") ||
		strings.Contains(errStr, "invalidtoken") ||
		strings.Contains(errStr, "token expired") ||
		strings.Contains(errStr, "unauthorized")
}

func cleanRoleName(roleName string) string {
	roleName = strings.TrimPrefix(roleName, "AWS-")
	roleName = strings.TrimPrefix(roleName, "AWS_")
	roleName = strings.TrimPrefix(roleName, "aws-")
	roleName = strings.TrimPrefix(roleName, "aws_")
	roleName = strings.TrimPrefix(roleName, "Aws-")
	roleName = strings.TrimPrefix(roleName, "Aws_")
	roleName = strings.TrimPrefix(roleName, "AWS")
	roleName = strings.TrimPrefix(roleName, "aws")
	roleName = strings.TrimPrefix(roleName, "Aws")
	return roleName
}

func fetchAccounts(token, region, ssoSession string) ([]AccountItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config: %w", err)
	}

	ssoClient := sso.NewFromConfig(cfg, func(o *sso.Options) {
		o.Region = region
	})

	var accounts []AccountItem
	var nextToken *string

	for {
		input := &sso.ListAccountsInput{
			AccessToken: aws.String(token),
		}
		if nextToken != nil {
			input.NextToken = nextToken
		}

		output, err := ssoClient.ListAccounts(ctx, input)
		if err != nil {
			if isExpiredTokenError(err) {
				return nil, fmt.Errorf("access token has expired. Browser authentication required.", ssoSession)
			}
			return nil, fmt.Errorf("unable to list accounts: %w", err)
		}

		for _, account := range output.AccountList {
			accounts = append(accounts, AccountItem{
				AccountID:   aws.ToString(account.AccountId),
				AccountName: aws.ToString(account.AccountName),
				Email:       aws.ToString(account.EmailAddress),
			})
		}

		if output.NextToken == nil {
			break
		}
		nextToken = output.NextToken
	}

	// Sort by account name
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].AccountName < accounts[j].AccountName
	})

	return accounts, nil
}

func fetchAccountRoles(token, region, accountID, ssoSession string) ([]ssoTypes.RoleInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config: %w", err)
	}

	ssoClient := sso.NewFromConfig(cfg, func(o *sso.Options) {
		o.Region = region
	})

	var roles []ssoTypes.RoleInfo
	var nextToken *string

	for {
		input := &sso.ListAccountRolesInput{
			AccessToken: aws.String(token),
			AccountId:   aws.String(accountID),
		}
		if nextToken != nil {
			input.NextToken = nextToken
		}

		output, err := ssoClient.ListAccountRoles(ctx, input)
		if err != nil {
			if isExpiredTokenError(err) {
				return nil, fmt.Errorf("access token has expired. Browser authentication required.", ssoSession)
			}
			return nil, fmt.Errorf("unable to list roles: %w", err)
		}

		roles = append(roles, output.RoleList...)

		if output.NextToken == nil {
			break
		}
		nextToken = output.NextToken
	}

	return roles, nil
}

type accountsLoadedMsg struct {
	accounts []AccountItem
}

type rolesLoadedMsg struct {
	account  AccountItem
	roles    []ssoTypes.RoleInfo
	roleItem RoleItem
}

type loginCompletedMsg struct{}

type configureMsg struct {
	account          AccountItem
	roles            []ssoTypes.RoleInfo
	roleItem         RoleItem
	originalRoleName string
}

type configuredMsg struct {
	profileName string
	output      string
}

func (m model) Init() tea.Cmd {
	return func() tea.Msg {
		token, err := getSSOTokenFromCache(m.ssoSession)
		if err != nil {
			return errMsg{err: fmt.Errorf("token not found. Browser authentication required.")}
		}

		m.accessToken = token

		accounts, err := fetchAccounts(token, m.region, m.ssoSession)
		if err != nil {
			return errMsg{err: err}
		}

		return accountsLoadedMsg{accounts: accounts}
	}
}

type errMsg struct {
	err error
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.state == stateSelecting && m.list.FilterState() == list.Filtering {
				// Don't quit when filtering, let the list handle 'q' for input
			} else {
				return m, tea.Quit
			}
		case "enter":
			if m.state == stateSelecting && m.list.SelectedItem() != nil {
				selectedAccount := m.list.SelectedItem().(AccountItem)
				m.state = stateFetchingRoles
				return m, m.fetchRolesAndConfigure(selectedAccount)
			}
			if m.state == stateSelectingRole && m.roleList.SelectedItem() != nil {
				selectedRole := m.roleList.SelectedItem().(RoleItem)
				m.selectedRole = selectedRole
				m.state = stateConfiguring
				selectedAccount := m.list.SelectedItem().(AccountItem)
				m.selectedAccount = selectedAccount
				var originalRoleName string
				for _, r := range m.roles {
					if cleanRoleName(aws.ToString(r.RoleName)) == cleanRoleName(selectedRole.RoleName) {
						originalRoleName = aws.ToString(r.RoleName)
						break
					}
				}
				if originalRoleName == "" {
					originalRoleName = aws.ToString(m.roles[0].RoleName)
				}
				return m, m.configureAWSProfile(selectedAccount, selectedRole, originalRoleName)
			}
		case "esc":
			if m.state == stateSelectingRole {
				m.state = stateSelecting
				m.roleList = list.New(m.list.Items(), NewRoleDelegate(), 0, 0)
				m.roleList.SetSize(m.width, m.height-3)
				return m, nil
			}
			if m.state == stateDone {
				m.state = stateSelecting
				items := make([]list.Item, len(m.accounts))
				for i, acc := range m.accounts {
					items[i] = acc
				}
				m.list.SetItems(items)
				m.list.Title = fmt.Sprintf("Select AWS Account (%d available)", len(m.accounts))
				return m, nil
			}
		}

	case accountsLoadedMsg:
		m.accounts = msg.accounts
		m.state = stateSelecting

		items := make([]list.Item, len(msg.accounts))
		for i, acc := range msg.accounts {
			items[i] = acc
		}

		m.list.SetItems(items)
		m.list.Title = fmt.Sprintf("Select AWS Account (%d available)", len(msg.accounts))
		return m, nil

	case rolesLoadedMsg:
		m.roles = msg.roles
		m.state = stateSelectingRole
		items := make([]list.Item, len(msg.roles))
		for i, r := range msg.roles {
			items[i] = RoleItem{RoleName: cleanRoleName(aws.ToString(r.RoleName))}
		}
		m.roleList.SetItems(items)
		return m, nil

	case errMsg:
		m.state = stateError
		m.err = msg.err
		return m, nil

	case configureMsg:
		m.selectedAccount = msg.account
		m.selectedRole = msg.roleItem
		m.roles = msg.roles
		m.state = stateConfiguring
		return m, m.configureAWSProfile(msg.account, msg.roleItem, msg.originalRoleName)

	case configuredMsg:
		m.state = stateDone
		m.profileName = msg.profileName
		m.outputMessage = msg.output
		if m.dryRun {
			m.statusMessage = "AWS profile would be configured (dry-run)"
		} else {
			m.statusMessage = "AWS profile configured successfully!"
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		listHeight := msg.Height - 3
		if listHeight < 3 {
			listHeight = 3
		}
		m.list.SetSize(msg.Width, listHeight)
		m.roleList.SetSize(msg.Width, listHeight)
		return m, nil
	}

	var cmd tea.Cmd
	if m.state == stateSelectingRole {
		m.roleList, cmd = m.roleList.Update(msg)
	} else {
		m.list, cmd = m.list.Update(msg)
	}
	return m, cmd
}

func (m model) View() string {
	switch m.state {
	case stateLoading:
		return titleStyle.Render("🔄 Loading AWS SSO Accounts...") + "\n\nPlease wait while we fetch your accounts.\n"

	case stateSelecting:
		if len(m.accounts) == 0 {
			return "No accounts available"
		}
		filterText := m.list.FilterInput.Value()
		filteredCount := len(m.list.VisibleItems())
		totalCount := len(m.accounts)
		isFiltering := m.list.FilterState() == list.Filtering

		var status string
		if isFiltering {
			status = fmt.Sprintf("  %s %d/%d items", m.list.FilterInput.View(), m.list.Index()+1, filteredCount)
		} else if filterText != "" {
			status = fmt.Sprintf("  Filter: \"%s\" • %d/%d items", filterText, m.list.Index()+1, filteredCount)
		} else if filteredCount < totalCount {
			status = fmt.Sprintf("  %d/%d items (%d filtered)", m.list.Index()+1, filteredCount, totalCount-filteredCount)
		} else {
			status = fmt.Sprintf("  %d/%d items", m.list.Index()+1, totalCount)
		}
		statusBar := helpStyle.Render(status)

		header := titleStyle.Render(fmt.Sprintf("  Select AWS Account (%d available)  ", totalCount))
		helpText := helpStyle.Render("↑/k ↓/j • navigate | / • filter | enter • select | q • quit")

		var parts []string
		parts = append(parts, header)
		parts = append(parts, m.list.View())
		parts = append(parts, statusBar)
		parts = append(parts, helpText)

		return strings.Join(parts, "\n")

	case stateFetchingRoles:
		if item := m.list.SelectedItem(); item == nil {
			return "No account selected"
		}
		selectedAccount := m.list.SelectedItem().(AccountItem)
		return titleStyle.Render("🔄 Fetching roles...") + "\n\n" +
			fmt.Sprintf("Account: %s (%s)\n", selectedAccount.AccountName, selectedAccount.AccountID) +
			"\n" + helpStyle.Render("Please wait...") + "\n"

	case stateConfiguring:
		return titleStyle.Render("⚙️  Configuring AWS Profile...") + "\n\n" +
			fmt.Sprintf("Account Name: %s\n", m.selectedAccount.AccountName) +
			fmt.Sprintf("Account ID: %s\n", m.selectedAccount.AccountID) +
			fmt.Sprintf("Email: %s\n", m.selectedAccount.Email) +
			fmt.Sprintf("Role: %s\n", m.selectedRole.RoleName) +
			"\n" + helpStyle.Render("Please wait...") + "\n"

	case stateSelectingRole:
		if item := m.list.SelectedItem(); item == nil {
			return "No account selected"
		}
		selectedAccount := m.list.SelectedItem().(AccountItem)
		roleCount := len(m.roles)
		roleIndex := m.roleList.Index() + 1

		status := fmt.Sprintf("  %d/%d roles", roleIndex, roleCount)
		statusBar := helpStyle.Render(status)

		header := titleStyle.Render(fmt.Sprintf("  Select Role for %s  ", selectedAccount.AccountName))
		helpText := helpStyle.Render("↑/k ↓/j • navigate | enter • select | esc • back | q • quit")

		var parts []string
		parts = append(parts, header)
		parts = append(parts, m.roleList.View())
		parts = append(parts, statusBar)
		parts = append(parts, helpText)

		return strings.Join(parts, "\n")

	case stateDone:
		if m.outputMessage != "" {
			return m.outputMessage + "\n" + helpStyle.Render("Press esc to go back or q to quit")
		}
		result := successStyle.Render("✓ "+m.statusMessage) + "\n\n"
		result += "Profile Details:\n"
		result += fmt.Sprintf("  Profile Name: %s\n", m.profileName)
		result += fmt.Sprintf("  Account: %s (%s)\n", m.selectedAccount.AccountName, m.selectedAccount.AccountID)
		result += fmt.Sprintf("  Role: %s\n", m.selectedRole.RoleName)
		result += fmt.Sprintf("  Region: %s\n\n", m.region)
		result += helpStyle.Render("Press esc to go back or q to quit")
		return result

	case stateError:
		return errorStyle.Render("✗ Error: "+m.err.Error()) + "\n" +
			helpStyle.Render("Press q to quit\n")
	}

	return ""
}

func (m model) fetchRolesAndConfigure(account AccountItem) tea.Cmd {
	return func() tea.Msg {
		roles, err := fetchAccountRoles(m.accessToken, m.region, account.AccountID, m.ssoSession)
		if err != nil {
			return errMsg{err: fmt.Errorf("failed to fetch roles: %w", err)}
		}

		if len(roles) == 0 {
			return errMsg{err: fmt.Errorf("no roles found for account %s", account.AccountID)}
		}

		if len(roles) == 1 {
			originalRoleName := aws.ToString(roles[0].RoleName)
			displayRoleName := cleanRoleName(originalRoleName)
			roleItem := RoleItem{RoleName: displayRoleName}
			return configureMsg{
				account:          account,
				roles:            roles,
				roleItem:         roleItem,
				originalRoleName: originalRoleName,
			}
		}

		return rolesLoadedMsg{account: account, roles: roles}
	}
}

func (m model) configureAWSProfile(account AccountItem, roleItem RoleItem, originalRoleName string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		selectedAccount := account
		selectedRole := roleItem

		homeDir, _ := os.UserHomeDir()
		configPath := filepath.Join(homeDir, ".aws", "config")
		// Create profile name (match existing AWS profile convention: PascalCase)
		// Extract alias from first word of account name
		parts := strings.Split(selectedAccount.AccountName, " ")
		alias := parts[0]
		accountName := strings.Join(parts[1:], "")
		roleName := cleanRoleName(selectedRole.RoleName)
		// Build the full profile name
		profileName := fmt.Sprintf("%s%s%s", alias, accountName, roleName)
		// Strip configured prefix/suffix from the profile name
		if m.namePrefix != "" {
			profileName = strings.TrimPrefix(profileName, m.namePrefix)
		}
		if m.nameSuffix != "" {
			profileName = strings.TrimSuffix(profileName, m.nameSuffix)
		}

		// Read existing config
		existingConfig, err := os.ReadFile(configPath)
		if err != nil {
			existingConfig = []byte{}
		}

		configContent := string(existingConfig)

		// Parse existing profiles
		profiles := parseProfiles(configContent)

		// Find if there's an existing profile with same account/session/role/region/output
		var existingProfileName string
		for _, p := range profiles {
			if p.SSOAccountID == selectedAccount.AccountID && p.SSOSession == m.ssoSession && p.SSORoleName == originalRoleName && p.Region == m.region && p.OutputFormat == m.outputFormat {
				existingProfileName = p.Name
				break
			}
		}

		// Check if the standardized profile name is already used by a different configuration
		for _, p := range profiles {
			if p.Name == profileName {
				if !(p.SSOAccountID == selectedAccount.AccountID && p.SSOSession == m.ssoSession && p.SSORoleName == originalRoleName && p.Region == m.region && p.OutputFormat == m.outputFormat) {
					return errMsg{err: fmt.Errorf("profile name '%s' is already used by a different configuration", profileName)}
				}
			}
		}

		// Remove any existing profile with the same account/session/role/region
		if existingProfileName != "" {
			configContent = removeSection(configContent, fmt.Sprintf("[profile %s]", existingProfileName))
		}

		profileMarker := fmt.Sprintf("[profile %s]", profileName)

		// Remove existing profile with the standardized name if present (though unlikely after above check)
		if strings.Contains(configContent, profileMarker) {
			configContent = removeSection(configContent, profileMarker)
		}

		// Append new profile
		newProfile := fmt.Sprintf(`
[profile %s]
sso_session = %s
sso_account_id = %s
sso_role_name = %s
region = %s
output = %s
`, profileName, m.ssoSession, selectedAccount.AccountID, originalRoleName, m.region, m.outputFormat)

		configContent += newProfile

		// Write back to config file
		if !m.dryRun {
			if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
				return errMsg{err: fmt.Errorf("failed to write AWS config: %w", err)}
			}
		}

		// Get role credentials and write credentials file (only when not dry-run)
		if m.writeCreds && !m.dryRun {
			// Get role credentials
			ssoCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(m.ssoRegion))
			if err != nil {
				return errMsg{err: fmt.Errorf("failed to load config for sso client: %w", err)}
			}
			ssoClient := sso.NewFromConfig(ssoCfg)

			roleCredentials, err := ssoClient.GetRoleCredentials(ctx, &sso.GetRoleCredentialsInput{
				AccessToken: aws.String(m.accessToken),
				AccountId:   aws.String(selectedAccount.AccountID),
				RoleName:    aws.String(originalRoleName),
			})
			if err != nil {
				if isExpiredTokenError(err) {
					return errMsg{err: fmt.Errorf("access token has expired. Browser authentication required.")}
				}
				return errMsg{err: fmt.Errorf("failed to get role credentials: %w", err)}
			}

			// Create credentials file
			credentialsPath := filepath.Join(homeDir, ".aws", "credentials")
			existingCreds, err := os.ReadFile(credentialsPath)
			if err != nil {
				existingCreds = []byte{}
			}

			credsContent := string(existingCreds)
			credsMarker := fmt.Sprintf("[%s]", profileName)

			// Remove old credentials for the standardized profile name
			if strings.Contains(credsContent, credsMarker) {
				credsContent = removeSection(credsContent, credsMarker)
			}

			// Also remove credentials for the existing profile if it had a different name
			if existingProfileName != "" && existingProfileName != profileName {
				oldCredsMarker := fmt.Sprintf("[%s]", existingProfileName)
				if strings.Contains(credsContent, oldCredsMarker) {
					credsContent = removeSection(credsContent, oldCredsMarker)
				}
			}

			// Add new credentials
			credsContent += fmt.Sprintf(`
[%s]
aws_access_key_id = %s
aws_secret_access_key = %s
aws_session_token = %s
`, profileName,
				aws.ToString(roleCredentials.RoleCredentials.AccessKeyId),
				aws.ToString(roleCredentials.RoleCredentials.SecretAccessKey),
				aws.ToString(roleCredentials.RoleCredentials.SessionToken))

			if err := os.WriteFile(credentialsPath, []byte(credsContent), 0600); err != nil {
				return errMsg{err: fmt.Errorf("failed to write AWS credentials: %w", err)}
			}
		}

		if m.dryRun {
			m.statusMessage = fmt.Sprintf("Profile '%s' would be configured (dry-run)", profileName)
		} else {
			m.statusMessage = fmt.Sprintf("Profile '%s' configured successfully!", profileName)
		}

		// Build output string
		output := "\n"
		if m.dryRun {
			output += lipgloss.NewStyle().Foreground(lipgloss.Color("#FFA500")).Bold(true).Render("⚠ DRY RUN - No files were modified")
		} else {
			output += successStyle.Render("✓ AWS Profile Configured!")
		}
		output += fmt.Sprintf("\n\nProfile Name: %s", lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFDF5")).Bold(true).Render(profileName))
		output += fmt.Sprintf("\nAccount: %s (%s, %s)", selectedAccount.AccountName, selectedAccount.AccountID, selectedAccount.Email)
		output += fmt.Sprintf("\nRole: %s (%s)", selectedRole.RoleName, originalRoleName)
		output += fmt.Sprintf("\nSSO Session: %s", m.ssoSession)
		output += fmt.Sprintf("\nRegion: %s", m.region)
		output += fmt.Sprintf("\nConfig File: %s", configPath)
		if m.writeCreds {
			credentialsPath := filepath.Join(homeDir, ".aws", "credentials")
			output += fmt.Sprintf("\nCredentials File: %s", credentialsPath)
		}
		if m.dryRun {
			output += fmt.Sprintf("\n\nNote: Profile will be written to '%s' when run without --dry-run", configPath)
		} else {
			output += "\n\nUsage Instructions:"
			output += fmt.Sprintf("\n  - Set the profile: export AWS_PROFILE=%s", profileName)
			output += fmt.Sprintf("\n  - List S3 buckets: aws s3 ls --profile %s", profileName)
			output += fmt.Sprintf("\n  - Or use directly: AWS_PROFILE=%s aws s3 ls", profileName)
		}
		output += "\n"

		return configuredMsg{profileName: profileName, output: output}
	}
}

func removeSection(content, sectionHeader string) string {
	lines := strings.Split(content, "\n")
	newLines := []string{}
	skip := false
	for _, line := range lines {
		if strings.TrimSpace(line) == sectionHeader {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(strings.TrimSpace(line), "[") {
			skip = false
		}
		if !skip {
			newLines = append(newLines, line)
		}
	}
	return strings.Join(newLines, "\n")
}

func parseProfiles(content string) []Profile {
	lines := strings.Split(content, "\n")
	var profiles []Profile
	var current Profile
	inProfile := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[profile ") && strings.HasSuffix(line, "]") {
			if inProfile {
				profiles = append(profiles, current)
			}
			name := strings.TrimSuffix(strings.TrimPrefix(line, "[profile "), "]")
			current = Profile{Name: name}
			inProfile = true
		} else if inProfile && strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				switch key {
				case "sso_account_id":
					current.SSOAccountID = value
				case "sso_session":
					current.SSOSession = value
				case "sso_role_name":
					current.SSORoleName = value
				case "region":
					current.Region = value
				case "output":
					current.OutputFormat = value
				}
			}
		} else if inProfile && line == "" {
			// skip empty lines
		} else if inProfile && strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "[profile ") {
			profiles = append(profiles, current)
			inProfile = false
		}
	}
	if inProfile {
		profiles = append(profiles, current)
	}
	return profiles
}

func cleanConfig(dryRun bool) {
	var credsExists bool
	var credsCleaned bool
	var credsContent string
	var originalCreds string

	fmt.Println("Cleaning corrupted profile entries and removing duplicates...")

	if dryRun {
		fmt.Println(helpStyle.Render("⚠ DRY RUN Mode - no files will be modified"))
		fmt.Println()
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Error: %v", err)))
		os.Exit(1)
	}

	configPath := filepath.Join(homeDir, ".aws", "config")
	credsPath := filepath.Join(homeDir, ".aws", "credentials")

	if _, err := os.Stat(credsPath); os.IsNotExist(err) {
		credsExists = false
		fmt.Println("Credentials file does not exist, skipping credentials processing.")
	} else if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Error checking credentials file: %v", err)))
		credsExists = false
	} else {
		credsExists = true
	}

	// Read and clean config file
	configData, err := os.ReadFile(configPath)
	configCleaned := false
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Error reading config: %v", err)))
		return
	}
	configContent := string(configData)
	originalConfig := configContent
	configContent = strings.ReplaceAll(configContent, "[[profile ", "[profile ")
	configContent = strings.ReplaceAll(configContent, "]]", "]")
	if configContent != originalConfig {
		configCleaned = true
	}

	if credsExists {
		// Read and clean credentials file
		credsData, err := os.ReadFile(credsPath)
		credsCleaned = false
		if err != nil {
			fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Error reading credentials: %v", err)))
			return
		}
		credsContent = string(credsData)
		originalCreds = credsContent
		credsContent = strings.ReplaceAll(credsContent, "[[profile ", "[")
		credsContent = strings.ReplaceAll(credsContent, "]]", "]")
		if credsContent != originalCreds {
			credsCleaned = true
		}
	} else {
		credsCleaned = false
		originalCreds = ""
		credsContent = ""
	}

	// Parse profiles from cleaned config
	profiles := parseProfiles(configContent)

	// Group profiles by sso_account_id, sso_session, sso_role_name, region
	group := make(map[string][]Profile)
	for _, p := range profiles {
		key := fmt.Sprintf("%s|%s|%s|%s", p.SSOAccountID, p.SSOSession, p.SSORoleName, p.Region)
		group[key] = append(group[key], p)
	}

	// Find duplicates
	var duplicates []string
	for _, ps := range group {
		if len(ps) > 1 {
			for i := 1; i < len(ps); i++ {
				duplicates = append(duplicates, ps[i].Name)
			}
		}
	}

	// Remove duplicate sections from config and credentials
	for _, dup := range duplicates {
		configContent = removeSection(configContent, fmt.Sprintf("[profile %s]", dup))
		if credsExists {
			credsContent = removeSection(credsContent, fmt.Sprintf("[%s]", dup))
		}
	}

	// Write back config if changed
	if configContent != originalConfig {
		if dryRun {
			fmt.Println(helpStyle.Render("  [dry-run] Would write cleaned config"))
		} else {
			if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
				fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Error writing config: %v", err)))
			} else {
				if configCleaned {
					fmt.Println(successStyle.Render("✓ Config cleaned and duplicates removed"))
				} else {
					fmt.Println(successStyle.Render("✓ Duplicate profiles removed from config"))
				}
			}
		}
	} else {
		fmt.Println("Config already clean")
	}

	// Write back credentials if changed
	if credsExists {
		if credsContent != originalCreds {
			if dryRun {
				fmt.Println(helpStyle.Render("  [dry-run] Would write cleaned credentials"))
			} else {
				if err := os.WriteFile(credsPath, []byte(credsContent), 0600); err != nil {
					fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Error writing credentials: %v", err)))
				} else {
					if credsCleaned {
						fmt.Println(successStyle.Render("✓ Credentials cleaned and duplicates removed"))
					} else {
						fmt.Println(successStyle.Render("✓ Duplicate profiles removed from credentials"))
					}
				}
			}
		} else {
			fmt.Println("Credentials already clean")
		}
	}

	// Report duplicates removed
	if len(duplicates) > 0 {
		if dryRun {
			fmt.Printf("Would remove %d duplicate profile(s): %s\n", len(duplicates), strings.Join(duplicates, ", "))
		} else {
			fmt.Printf("Removed %d duplicate profile(s): %s\n", len(duplicates), strings.Join(duplicates, ", "))
		}
	} else {
		fmt.Println("No duplicate profiles found")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func runSSOLogin(ssoSession string) error {
	cmd := exec.Command("aws", "sso", "login", "--sso-session", ssoSession)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func withTokenRefresh(ssoSession string, fn func(token string) error) error {
	token, err := getSSOTokenFromCache(ssoSession)
	if err != nil {
		if strings.Contains(err.Error(), "expired") {
			fmt.Println("Token expired, refreshing...")
			if err := runSSOLogin(ssoSession); err != nil {
				return fmt.Errorf("failed to refresh token: %w", err)
			}
			token, err = getSSOTokenFromCache(ssoSession)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return fn(token)
}

func configureAllProfiles(ssoSession, region, ssoRegion, outputFormat string, writeCreds, dryRun bool, namePrefix, nameSuffix string) {
	fmt.Printf("Checking SSO session '%s'...\n", ssoSession)

	if dryRun {
		fmt.Println(helpStyle.Render("⚠ DRY RUN Mode - no files will be modified"))
		fmt.Println()
	}

	token, err := getSSOTokenFromCache(ssoSession)
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("✗ %v", err)))
		fmt.Println("\nAttempting to login...")
		if err := runSSOLogin(ssoSession); err != nil {
			fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Login failed: %v", err)))
			os.Exit(1)
		}
		token, err = getSSOTokenFromCache(ssoSession)
		if err != nil {
			fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Still failed to get token: %v", err)))
			os.Exit(1)
		}
	}
	fmt.Println(successStyle.Render("✓ SSO token found"))
	fmt.Println()

	fmt.Println("Fetching accounts...")
	accounts, err := fetchAccounts(token, region, ssoSession)
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Failed to fetch accounts: %v", err)))
		os.Exit(1)
	}
	fmt.Printf("Found %d accounts\n\n", len(accounts))

	homeDir, _ := os.UserHomeDir()
	configPath := filepath.Join(homeDir, ".aws", "config")

	var configured, skipped, failed int

	for _, account := range accounts {
		fmt.Printf("→ %s (%s)\n", account.AccountName, account.AccountID)

		roles, err := fetchAccountRoles(token, region, account.AccountID, ssoSession)
		if err != nil {
			fmt.Printf("  %s\n", errorStyle.Render(fmt.Sprintf("✗ Failed to fetch roles: %v", err)))
			failed++
			continue
		}

		if len(roles) == 0 {
			fmt.Printf("  %s\n", helpStyle.Render("No roles found, skipping"))
			skipped++
			continue
		}

		for _, role := range roles {
			originalRoleName := aws.ToString(role.RoleName)
			displayRoleName := cleanRoleName(originalRoleName)

			// Generate standardized profile name
			parts := strings.Split(account.AccountName, " ")
			alias := parts[0]
			accountName := strings.Join(parts[1:], "")
			roleName := cleanRoleName(displayRoleName)
			profileName := fmt.Sprintf("%s%s%s", alias, accountName, roleName)
			if namePrefix != "" {
				profileName = strings.TrimPrefix(profileName, namePrefix)
			}
			if nameSuffix != "" {
				profileName = strings.TrimSuffix(profileName, nameSuffix)
			}

			// Read current config
			existingConfig, err := os.ReadFile(configPath)
			if err != nil {
				existingConfig = []byte{}
			}
			configContent := string(existingConfig)

			// Parse existing profiles
			profiles := parseProfiles(configContent)

			// Check for existing profile with same account/session/role/region
			var existingProfileName string
			for _, p := range profiles {
				if p.SSOAccountID == account.AccountID && p.SSOSession == ssoSession && p.SSORoleName == originalRoleName && p.Region == region {
					existingProfileName = p.Name
					break
				}
			}

			// Check name conflict
			nameConflict := false
			for _, p := range profiles {
				if p.Name == profileName && !(p.SSOAccountID == account.AccountID && p.SSOSession == ssoSession && p.SSORoleName == originalRoleName && p.Region == region) {
					fmt.Printf("  %s\n", errorStyle.Render(fmt.Sprintf("✗ %s: profile name conflict with different config", displayRoleName)))
					nameConflict = true
					failed++
					break
				}
			}
			if nameConflict {
				continue
			}

			// If existing profile matches and already has the standardized name, skip
			if existingProfileName == profileName {
				fmt.Printf("  %s\n", helpStyle.Render(fmt.Sprintf("- %s: already configured as '%s'", displayRoleName, profileName)))
				skipped++
				continue
			}

			// Remove old profile with same config (different name)
			if existingProfileName != "" {
				configContent = removeSection(configContent, fmt.Sprintf("[profile %s]", existingProfileName))
				fmt.Printf("  %s\n", helpStyle.Render(fmt.Sprintf("  Replacing old profile '%s'", existingProfileName)))
			}

			// Remove existing profile with standardized name
			profileMarker := fmt.Sprintf("[profile %s]", profileName)
			if strings.Contains(configContent, profileMarker) {
				configContent = removeSection(configContent, profileMarker)
			}

			// Append new profile
			newProfile := fmt.Sprintf("\n[profile %s]\nsso_session = %s\nsso_account_id = %s\nsso_role_name = %s\nregion = %s\noutput = %s\n", profileName, ssoSession, account.AccountID, originalRoleName, region, outputFormat)
			configContent += newProfile

			if !dryRun {
				if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
					fmt.Printf("  %s\n", errorStyle.Render(fmt.Sprintf("✗ %s: failed to write config: %v", displayRoleName, err)))
					failed++
					continue
				}
			}

			// Handle credentials if requested
			if writeCreds && !dryRun {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				ssoCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(ssoRegion))
				if err == nil {
					ssoClient := sso.NewFromConfig(ssoCfg)
					roleCredentials, err := ssoClient.GetRoleCredentials(ctx, &sso.GetRoleCredentialsInput{
						AccessToken: aws.String(token),
						AccountId:   aws.String(account.AccountID),
						RoleName:    aws.String(originalRoleName),
					})
					if err == nil {
						credentialsPath := filepath.Join(homeDir, ".aws", "credentials")
						existingCreds, err := os.ReadFile(credentialsPath)
						if err != nil {
							existingCreds = []byte{}
						}
						credsContent := string(existingCreds)
						credsMarker := fmt.Sprintf("[%s]", profileName)
						if strings.Contains(credsContent, credsMarker) {
							credsContent = removeSection(credsContent, credsMarker)
						}
						if existingProfileName != "" && existingProfileName != profileName {
							oldCredsMarker := fmt.Sprintf("[%s]", existingProfileName)
							if strings.Contains(credsContent, oldCredsMarker) {
								credsContent = removeSection(credsContent, oldCredsMarker)
							}
						}
						credsContent += fmt.Sprintf("\n[%s]\naws_access_key_id = %s\naws_secret_access_key = %s\naws_session_token = %s\n", profileName,
							aws.ToString(roleCredentials.RoleCredentials.AccessKeyId),
							aws.ToString(roleCredentials.RoleCredentials.SecretAccessKey),
							aws.ToString(roleCredentials.RoleCredentials.SessionToken))
						os.WriteFile(credentialsPath, []byte(credsContent), 0600)
					}
				}
				cancel()
			}

			if dryRun {
				if existingProfileName != "" {
					fmt.Printf("  %s\n", helpStyle.Render(fmt.Sprintf("- %s → '%s' (would replace '%s')", displayRoleName, profileName, existingProfileName)))
				} else {
					fmt.Printf("  %s\n", helpStyle.Render(fmt.Sprintf("- %s → '%s' (would configure)", displayRoleName, profileName)))
				}
			} else {
				if existingProfileName != "" {
					fmt.Printf("  %s\n", successStyle.Render(fmt.Sprintf("✓ %s → '%s' (replaced '%s')", displayRoleName, profileName, existingProfileName)))
				} else {
					fmt.Printf("  %s\n", successStyle.Render(fmt.Sprintf("✓ %s → '%s'", displayRoleName, profileName)))
				}
			}
			configured++
		}
	}

	if dryRun {
		fmt.Println(helpStyle.Render(fmt.Sprintf("Done (dry-run)! Would configure: %d, Skipped: %d, Failed: %d", configured, skipped, failed)))
	} else {
		fmt.Println(successStyle.Render(fmt.Sprintf("Done! Configured: %d, Skipped: %d, Failed: %d", configured, skipped, failed)))
	}
}

func main() {
	loadDefaultConfig()

	// Check if user wants to see help before parsing flags
	for _, arg := range os.Args[1:] {
		if arg == "-h" || arg == "--help" {
			fmt.Println("AWS SSO Account Selector")
			fmt.Println()
			fmt.Println("Usage:")
			fmt.Println("  ass                       # Run selector")
			fmt.Println("  ass --write-creds         # Write credentials to ~/.aws/credentials")
			fmt.Println("  ass --dry-run             # Show what would be configured without writing files")
			fmt.Println("  ass --output-format yaml  # Set output format (json, yaml)")
			fmt.Println("  ass all                   # Configure all accounts and roles")
			fmt.Println("  ass clean-config          # Clean corrupted profile entries in AWS config files")
			fmt.Println("  AWS_SSO_SESSION=bimb ass  # Specify session")
			fmt.Println("  AWS_REGION=us-east-1 ass  # Specify region")
			fmt.Println("  AWS_PROFILE_NAME_PREFIX=Org ass  # Strip prefix from generated profile name")
			fmt.Println("  AWS_PROFILE_NAME_SUFFIX=Role ass  # Strip suffix from generated profile name")
			fmt.Println()
			fmt.Println("Prerequisites:")
			fmt.Println("  1. AWS CLI v2 installed")
			fmt.Println("  2. SSO configured: aws configure sso")
			fmt.Println("  3. Logged in: aws sso login --sso-session <session>")
			fmt.Println()
			os.Exit(0)
		}
	}

	var writeCreds = flag.Bool("write-creds", false, "Write temporary credentials to ~/.aws/credentials")
	var dryRun = flag.Bool("dry-run", false, "Show what would be configured without writing files")
	var outputFormat string
	flag.StringVar(&outputFormat, "output-format", "yaml", "Output format for AWS CLI (json, yaml)")
	flag.StringVar(&outputFormat, "o", "yaml", "Output format (short for --output-format)")
	var namePrefix string
	var nameSuffix string
	flag.StringVar(&namePrefix, "name-prefix", "", "Prefix to strip from generated profile names (or set AWS_PROFILE_NAME_PREFIX)")
	flag.StringVar(&nameSuffix, "name-suffix", "", "Suffix to strip from generated profile names (or set AWS_PROFILE_NAME_SUFFIX)")
	flag.Parse()

	if namePrefix == "" {
		namePrefix = envOrConfig("AWS_PROFILE_NAME_PREFIX", defaultCfg.ProfileNamePrefix)
	}
	if nameSuffix == "" {
		nameSuffix = envOrConfig("AWS_PROFILE_NAME_SUFFIX", defaultCfg.ProfileNameSuffix)
	}

	// Get SSO session from environment or default
	ssoSession := envOrConfig("AWS_SSO_SESSION", defaultCfg.SSOSession)
	if ssoSession == "" {
		// Try to detect from AWS config
		homeDir, err := os.UserHomeDir()
		if err == nil {
			configPath := filepath.Join(homeDir, ".aws", "config")
			configData, err := os.ReadFile(configPath)
			if err == nil {
				lines := strings.Split(string(configData), "\n")
				for _, line := range lines {
					if strings.HasPrefix(line, "sso_session") {
						parts := strings.SplitN(line, "=", 2)
						if len(parts) == 2 {
							ssoSession = strings.TrimSpace(parts[1])
							break
						}
					}
				}
			}
		}
		if ssoSession == "" {
			ssoSession = "default" // Default fallback
		}
	}

	region := envOrConfig("AWS_REGION", defaultCfg.Region)
	if region == "" {
		region = "ap-southeast-1" // Default fallback
	}

	// Read ssoRegion from config
	ssoRegion := ""
	if ssoSession != "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			configPath := filepath.Join(homeDir, ".aws", "config")
			configData, err := os.ReadFile(configPath)
			if err == nil {
				lines := strings.Split(string(configData), "\n")
				inSessionSection := false
				for _, line := range lines {
					if strings.HasPrefix(line, "[sso-session "+ssoSession+"]") {
						inSessionSection = true
						continue
					}
					if inSessionSection {
						if strings.HasPrefix(line, "sso_region") {
							parts := strings.SplitN(line, "=", 2)
							if len(parts) == 2 {
								ssoRegion = strings.TrimSpace(parts[1])
							}
						}
						if strings.HasPrefix(line, "[") {
							break
						}
					}
				}
			}
		}
	}
	if ssoRegion == "" {
		ssoRegion = region
	}

	// Clean config command
	args := flag.Args()
	if len(args) > 0 && args[0] == "clean-config" {
		cleanConfig(*dryRun)
		os.Exit(0)
	}

	// Configure all profiles command
	if len(args) > 0 && args[0] == "all" {
		configureAllProfiles(ssoSession, region, ssoRegion, outputFormat, *writeCreds, *dryRun, namePrefix, nameSuffix)
		os.Exit(0)
	}

	// Check if user wants to login first
	fmt.Printf("Checking SSO session '%s'...\n", ssoSession)

	// Verify token exists before starting UI
	token, err := getSSOTokenFromCache(ssoSession)
	if err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("✗ %v", err)))
		fmt.Println("\nAWS SSO requires browser authentication.")
		fmt.Println("Attempting to login (a browser window will open)...")
		if err := runSSOLogin(ssoSession); err != nil {
			fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Login failed: %v", err)))
			fmt.Println("\nPlease run manually in another terminal:")
			fmt.Printf("  aws sso login --sso-session %s\n", ssoSession)
			os.Exit(1)
		}
		// Retry getting token after login
		token, err = getSSOTokenFromCache(ssoSession)
		if err != nil {
			fmt.Println(errorStyle.Render(fmt.Sprintf("✗ Still failed to get token: %v", err)))
			os.Exit(1)
		}
	}

	fmt.Println(successStyle.Render("✓ SSO token found"))
	fmt.Println()

	m := initialModel(ssoSession, region, ssoRegion, outputFormat, *writeCreds, *dryRun, namePrefix, nameSuffix)
	m.accessToken = token

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("Error: %v", err)))
		os.Exit(1)
	}
}

type defaultConfig struct {
	SSOSession        string `yaml:"sso_session"`
	Region            string `yaml:"region"`
	ProfileNamePrefix string `yaml:"profile_name_prefix"`
	ProfileNameSuffix string `yaml:"profile_name_suffix"`
}

var defaultCfg defaultConfig

func loadDefaultConfig() {
	for _, p := range defaultConfigPaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg defaultConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			continue
		}
		defaultCfg = cfg
		return
	}
}

func defaultConfigPaths() []string {
	paths := []string{}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "ass", "ass.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, "ass.yaml"))
	}
	return paths
}

func envOrConfig(envKey, fallback string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fallback
}
