package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"log"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	fyneApp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/docker/docker/api/types"
	dockerContainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerImage "github.com/docker/docker/api/types/image"
	dockerNetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

var (
	// Global Docker connection settings (default: Unix socket)
	dockerHost  = "unix:///var/run/docker.sock"
	tlsCAPath   = ""
	tlsCertPath = ""
	tlsKeyPath  = ""

	// Global Docker client and main window (for dialogs)
	dockerCli  *client.Client
	mainWindow fyne.Window

	// Global selection indices (only declared once)
	selectedContainerIndex = -1
	selectedImageIndex     = -1
	selectedVolumeIndex    = -1
	selectedNetworkIndex   = -1

	// Global app instance
	appInstance fyne.App
)

type iPhoneLikeTheme struct{}

func (i *iPhoneLikeTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		// Crisp white
		return color.RGBA{255, 255, 255, 255}
	case theme.ColorNameButton:
		// iOS system blue
		return color.RGBA{0, 122, 255, 255}
	case theme.ColorNameDisabledButton:
		return color.RGBA{220, 220, 220, 255}
	case theme.ColorNameForeground:
		// iOS black text
		return color.RGBA{28, 28, 30, 255}
	case theme.ColorNameDisabled:
		// Subtle gray
		return color.RGBA{142, 142, 147, 255}
	case theme.ColorNameHover:
		return color.RGBA{245, 245, 245, 255}
	case theme.ColorNameFocus:
		return color.RGBA{0, 122, 255, 255}
	case theme.ColorNameScrollBar:
		return color.RGBA{180, 180, 180, 180}
	default:
		return theme.DefaultTheme().Color(n, v)
	}
}

func (i *iPhoneLikeTheme) Font(style fyne.TextStyle) fyne.Resource {
	// If you embed SF fonts, return them here. Otherwise fallback:
	return theme.DefaultTheme().Font(style)
}

func (i *iPhoneLikeTheme) Icon(n fyne.ThemeIconName) fyne.Resource {
	// Possibly override with iOS-like icons if you want
	return theme.DefaultTheme().Icon(n)
}

func (i *iPhoneLikeTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 16
	case theme.SizeNameText:
		return 16
	default:
		return theme.DefaultTheme().Size(name)
	}
}

// createDockerClient creates (or recreates) the global Docker client.
func createDockerClient() error {
	opts := []client.Opt{
		client.WithAPIVersionNegotiation(),
	}
	if dockerHost != "" {
		opts = append(opts, client.WithHost(dockerHost))
	}
	if tlsCAPath != "" && tlsCertPath != "" && tlsKeyPath != "" {
		opts = append(opts, client.WithTLSClientConfig(tlsCAPath, tlsCertPath, tlsKeyPath))
	}
	var err error
	dockerCli, err = client.NewClientWithOpts(opts...)
	return err
}

func main() {
	appInstance = fyneApp.New()
	appInstance.Settings().SetTheme(&iPhoneLikeTheme{})

	mainWindow = appInstance.NewWindow("Docker Dashboard")
	mainWindow.Resize(fyne.NewSize(1200, 800))

	// Create Docker client.
	if err := createDockerClient(); err != nil {
		log.Fatal("Error creating Docker client:", err)
	}

	// Build tabs.
	containersTab := buildContainersTab(dockerCli)
	imagesTab := buildImagesTab(dockerCli)
	volumesTab := buildVolumesTab(dockerCli)
	networksTab := buildNetworksTab(dockerCli)
	settingsTab := buildSettingsTab()

	tabs := container.NewAppTabs(
		container.NewTabItem("Containers", containersTab),
		container.NewTabItem("Images", imagesTab),
		container.NewTabItem("Volumes", volumesTab),
		container.NewTabItem("Networks", networksTab),
		container.NewTabItem("Settings", settingsTab),
	)
	tabs.SetTabLocation(container.TabLocationTop)

	mainWindow.SetContent(tabs)
	mainWindow.ShowAndRun()
}

// =============================================================================
// Container Stats (Non-Streaming, One-Shot)
// =============================================================================

func showContainerStats(index int, cli *client.Client) {
	if index == -1 {
		return
	}
	containers, err := dockerCli.ContainerList(context.Background(), dockerContainer.ListOptions{All: true})
	if err != nil || index >= len(containers) {
		return
	}
	// Get one-shot stats.
	stats, err := dockerCli.ContainerStatsOneShot(context.Background(), containers[index].ID)
	if err != nil {
		log.Println("Error fetching container stats:", err)
		return
	}
	defer stats.Body.Close()

	var statsJSON types.StatsJSON
	decoder := json.NewDecoder(stats.Body)
	if err := decoder.Decode(&statsJSON); err != nil {
		log.Println("Error decoding stats:", err)
		return
	}

	cpuDelta := float64(statsJSON.CPUStats.CPUUsage.TotalUsage - statsJSON.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(statsJSON.CPUStats.SystemUsage - statsJSON.PreCPUStats.SystemUsage)
	cpuPercent := 0.0
	if systemDelta > 0 && cpuDelta > 0 {
		cpuPercent = (cpuDelta / systemDelta) * float64(len(statsJSON.CPUStats.CPUUsage.PercpuUsage)) * 100.0
	}

	memUsed := statsJSON.MemoryStats.Usage
	memLimit := statsJSON.MemoryStats.Limit
	memPercent := 0.0
	if memLimit > 0 {
		memPercent = (float64(memUsed) / float64(memLimit)) * 100.0
	}

	statsText := fmt.Sprintf("CPU Usage: %.2f%%\nMemory Usage: %d / %d (%.2f%%)",
		cpuPercent, memUsed, memLimit, memPercent)

	win := appInstance.NewWindow("Container Stats")
	lbl := widget.NewLabel(statsText)
	lbl.Wrapping = fyne.TextWrapWord
	win.SetContent(container.NewScroll(lbl))
	win.Resize(fyne.NewSize(300, 200))
	win.Show()
}

// =============================================================================
// Advanced Container Creation (Accordion Form)
// (Not hooked up to any tab by default.)
func showAdvancedContainerForm(parent fyne.Window) {
	// 1. Basic Section
	imageEntry := widget.NewEntry()
	imageEntry.SetText("alpine")
	imageHelp := widget.NewLabel("The Docker image name, e.g. 'alpine:latest'")

	cmdEntry := widget.NewEntry()
	cmdEntry.SetText("echo hello world")
	cmdHelp := widget.NewLabel("Command to run in the container.")

	basicForm := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Image", imageEntry),
			widget.NewFormItem("Command", cmdEntry),
		),
		imageHelp,
		cmdHelp,
	)

	// 2. Environment & Ports Section
	envContainer := container.NewVBox()
	envHelp := widget.NewLabel("Set environment variables (key=value). Add rows as needed.")
	addEnvBtn := widget.NewButton("Add Env", func() {
		row := newEnvVarRow(envContainer)
		envContainer.Add(row)
	})

	portsContainer := container.NewVBox()
	portsHelp := widget.NewLabel("Map host ports to container ports (hostPort -> containerPort).")
	addPortBtn := widget.NewButton("Add Port", func() {
		row := newPortMapRow(portsContainer)
		portsContainer.Add(row)
	})

	envPortsSection := container.NewVBox(
		envHelp,
		addEnvBtn,
		envContainer,
		widget.NewSeparator(),
		portsHelp,
		addPortBtn,
		portsContainer,
	)

	// 3. Advanced Section
	memoryEntry := widget.NewEntry()
	memoryEntry.SetPlaceHolder("e.g. 256 (MB)")
	memoryHelp := widget.NewLabel("Memory limit in MB. Leave blank for unlimited.")

	cpuSharesEntry := widget.NewEntry()
	cpuSharesEntry.SetPlaceHolder("e.g. 1024")
	cpuHelp := widget.NewLabel("Relative CPU weight. 1024 is default for one CPU.")

	privilegedCheck := widget.NewCheck("Privileged Mode", func(bool) {})
	privilegedHelp := widget.NewLabel("Grants extended privileges to this container.")

	advancedForm := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Memory (MB)", memoryEntry),
			widget.NewFormItem("CPU Shares", cpuSharesEntry),
		),
		memoryHelp,
		cpuHelp,
		widget.NewSeparator(),
		privilegedCheck,
		privilegedHelp,
	)

	accordion := widget.NewAccordion(
		widget.NewAccordionItem("Basic", basicForm),
		widget.NewAccordionItem("Environment & Ports", envPortsSection),
		widget.NewAccordionItem("Advanced", advancedForm),
	)
	for _, item := range accordion.Items {
		item.Open = true
	}

	submitBtn := widget.NewButton("Create Container", func() {
		image := imageEntry.Text
		cmdParts := strings.Fields(cmdEntry.Text)
		envVars := gatherEnvVars(envContainer)
		// (Ports gathering omitted for brevity.)
		var memoryLimit int64
		if memoryEntry.Text != "" {
			mb, err := strconv.ParseInt(memoryEntry.Text, 10, 64)
			if err == nil && mb > 0 {
				memoryLimit = mb * 1024 * 1024
			}
		}
		var cpuShares int64
		if cpuSharesEntry.Text != "" {
			shares, err := strconv.ParseInt(cpuSharesEntry.Text, 10, 64)
			if err == nil && shares > 0 {
				cpuShares = shares
			}
		}
		privileged := privilegedCheck.Checked

		_, err := dockerCli.ImagePull(context.Background(), image, dockerImage.PullOptions{})
		if err != nil {
			dialog.ShowError(err, parent)
			return
		}

		resp, err := dockerCli.ContainerCreate(context.Background(),
			&dockerContainer.Config{
				Image: image,
				Cmd:   cmdParts,
				Env:   envVars,
			},
			&dockerContainer.HostConfig{
				Privileged: privileged,
				Resources: dockerContainer.Resources{
					Memory:    memoryLimit,
					CPUShares: cpuShares,
				},
			},
			nil, nil, "",
		)
		if err != nil {
			dialog.ShowError(err, parent)
			return
		}

		if err := dockerCli.ContainerStart(context.Background(), resp.ID, dockerContainer.StartOptions{}); err != nil {
			dialog.ShowError(err, parent)
			return
		}
		dialog.ShowInformation("Success", "Container created and started!", parent)
		parent.Close()
	})

	content := container.NewVBox(accordion, submitBtn)
	parent.SetContent(content)
}

// =============================================================================
// Helper Functions for Advanced Form
// =============================================================================

func newEnvVarRow(parent *fyne.Container) fyne.CanvasObject {
	keyEntry := widget.NewEntry()
	keyEntry.SetPlaceHolder("KEY")
	valEntry := widget.NewEntry()
	valEntry.SetPlaceHolder("value")
	rowBox := container.NewHBox(keyEntry, valEntry)
	removeBtn := widget.NewButton("Remove", func() {
		parent.Remove(rowBox)
	})
	rowBox.Add(removeBtn)
	return rowBox
}

func gatherEnvVars(envContainer *fyne.Container) []string {
	var result []string
	for _, child := range envContainer.Objects {
		if row, ok := child.(*fyne.Container); ok && len(row.Objects) >= 2 {
			if keyE, ok1 := row.Objects[0].(*widget.Entry); ok1 && keyE.Text != "" {
				var valText string
				if valE, ok2 := row.Objects[1].(*widget.Entry); ok2 {
					valText = valE.Text
				}
				result = append(result, fmt.Sprintf("%s=%s", keyE.Text, valText))
			}
		}
	}
	return result
}

func newPortMapRow(parent *fyne.Container) fyne.CanvasObject {
	hostEntry := widget.NewEntry()
	hostEntry.SetPlaceHolder("hostPort")
	containerEntry := widget.NewEntry()
	containerEntry.SetPlaceHolder("containerPort")
	rowBox := container.NewHBox(hostEntry, containerEntry)
	removeBtn := widget.NewButton("Remove", func() {
		parent.Remove(rowBox)
	})
	rowBox.Add(removeBtn)
	return rowBox
}

func gatherPortBindings(portsContainer *fyne.Container) (nat.PortMap, error) {
	portBindings := nat.PortMap{}
	for _, child := range portsContainer.Objects {
		if row, ok := child.(*fyne.Container); ok && len(row.Objects) >= 2 {
			hostE, ok1 := row.Objects[0].(*widget.Entry)
			contE, ok2 := row.Objects[1].(*widget.Entry)
			if !ok1 || !ok2 || hostE.Text == "" || contE.Text == "" {
				continue
			}
			containerPort := contE.Text + "/tcp"
			port := nat.Port(containerPort)
			portBindings[port] = append(portBindings[port], nat.PortBinding{
				HostIP:   "",
				HostPort: hostE.Text,
			})
		}
	}
	return portBindings, nil
}

// =============================================================================
// Settings Tab
// =============================================================================

func buildSettingsTab() fyne.CanvasObject {
	hostEntry := widget.NewEntry()
	hostEntry.SetText(dockerHost)
	caEntry := widget.NewEntry()
	caEntry.SetText(tlsCAPath)
	certEntry := widget.NewEntry()
	certEntry.SetText(tlsCertPath)
	keyEntry := widget.NewEntry()
	keyEntry.SetText(tlsKeyPath)

	form := widget.NewForm(
		widget.NewFormItem("Docker Host", hostEntry),
		widget.NewFormItem("CA Cert Path", caEntry),
		widget.NewFormItem("Client Cert Path", certEntry),
		widget.NewFormItem("Client Key Path", keyEntry),
	)
	form.OnSubmit = func() {
		dockerHost = hostEntry.Text
		tlsCAPath = caEntry.Text
		tlsCertPath = certEntry.Text
		tlsKeyPath = keyEntry.Text

		if err := createDockerClient(); err != nil {
			dialog.ShowError(err, mainWindow)
			return
		}
		dialog.ShowInformation("Settings", "Docker client updated successfully", mainWindow)
	}
	form.OnCancel = func() {}
	return form
}

// =============================================================================
// Containers Tab
// =============================================================================

func buildContainersTab(cli *client.Client) fyne.CanvasObject {
	var containerData []string

	containerList := widget.NewList(
		func() int { return len(containerData) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i int, obj fyne.CanvasObject) {
			obj.(*widget.Label).SetText(containerData[i])
		},
	)
	containerList.OnSelected = func(id int) {
		selectedContainerIndex = id
		fmt.Println("Selected container:", containerData[id])
	}

	refreshBtn := widget.NewButton("Refresh", func() {
		updateContainerList(&containerData, containerList, cli)
	})
	startBtn := widget.NewButton("Start", func() {
		startSelectedContainer(selectedContainerIndex, cli, &containerData, containerList)
	})
	stopBtn := widget.NewButton("Stop", func() {
		stopSelectedContainer(selectedContainerIndex, cli, &containerData, containerList)
	})
	logsBtn := widget.NewButton("Logs", func() {
		viewContainerLogs(selectedContainerIndex, cli)
	})
	removeBtn := widget.NewButton("Remove", func() {
		removeSelectedContainer(selectedContainerIndex, cli, &containerData, containerList)
	})
	inspectBtn := widget.NewButton("Inspect", func() {
		inspectSelectedContainer(selectedContainerIndex, cli)
	})
	statsBtn := widget.NewButton("Stats", func() {
		showContainerStats(selectedContainerIndex, cli)
	})
	runAlpineBtn := widget.NewButton("Run Alpine", func() {
		runAlpineContainer(cli, &containerData, containerList)
	})
	runCustomBtn := widget.NewButton("Run Custom Container", func() {
		showCustomContainerForm(cli, &containerData, containerList)
	})

	topRow := container.NewHBox(refreshBtn, startBtn, stopBtn, logsBtn, removeBtn)
	midRow := container.NewHBox(inspectBtn, statsBtn, runAlpineBtn, runCustomBtn)
	containerBox := container.NewVBox(containerList, topRow, midRow)
	updateContainerList(&containerData, containerList, cli)
	return containerBox
}

func updateContainerList(data *[]string, list *widget.List, cli *client.Client) {
	containers, err := cli.ContainerList(context.Background(), dockerContainer.ListOptions{All: true})
	if err != nil {
		log.Println("Error fetching containers:", err)
		return
	}
	*data = make([]string, len(containers))
	for i, c := range containers {
		(*data)[i] = fmt.Sprintf("ID:%s | Image:%s | Status:%s", c.ID[:12], c.Image, c.Status)
	}
	list.Refresh()
}

func startSelectedContainer(index int, cli *client.Client, data *[]string, list *widget.List) {
	if index == -1 {
		return
	}
	containers, err := cli.ContainerList(context.Background(), dockerContainer.ListOptions{All: true})
	if err != nil || index >= len(containers) {
		return
	}
	if err = cli.ContainerStart(context.Background(), containers[index].ID, dockerContainer.StartOptions{}); err != nil {
		log.Println("Error starting container:", err)
	}
	updateContainerList(data, list, cli)
}

func stopSelectedContainer(index int, cli *client.Client, data *[]string, list *widget.List) {
	if index == -1 {
		return
	}
	containers, err := cli.ContainerList(context.Background(), dockerContainer.ListOptions{All: true})
	if err != nil || index >= len(containers) {
		return
	}
	if err = cli.ContainerStop(context.Background(), containers[index].ID, dockerContainer.StopOptions{}); err != nil {
		log.Println("Error stopping container:", err)
	}
	updateContainerList(data, list, cli)
}

func viewContainerLogs(index int, cli *client.Client) {
	if index == -1 {
		return
	}
	containers, err := cli.ContainerList(context.Background(), dockerContainer.ListOptions{All: true})
	if err != nil || index >= len(containers) {
		return
	}
	reader, err := cli.ContainerLogs(context.Background(), containers[index].ID, dockerContainer.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "100",
	})
	if err != nil {
		log.Println("Error fetching logs:", err)
		return
	}
	defer reader.Close()
	logsData, err := io.ReadAll(reader)
	if err != nil {
		log.Println("Error reading logs:", err)
		return
	}
	showLogsInWindow(string(logsData))
}

func showLogsInWindow(logData string) {
	win := appInstance.NewWindow("Logs")
	lbl := widget.NewLabel(logData)
	lbl.Wrapping = fyne.TextWrapWord
	scroll := container.NewScroll(lbl)
	scroll.SetMinSize(fyne.NewSize(600, 400))
	win.SetContent(scroll)
	win.Show()
}

func removeSelectedContainer(index int, cli *client.Client, data *[]string, list *widget.List) {
	if index == -1 {
		return
	}
	containers, err := cli.ContainerList(context.Background(), dockerContainer.ListOptions{All: true})
	if err != nil || index >= len(containers) {
		return
	}
	if err = cli.ContainerRemove(context.Background(), containers[index].ID, dockerContainer.RemoveOptions{Force: true}); err != nil {
		log.Println("Error removing container:", err)
		return
	}
	updateContainerList(data, list, cli)
}

func inspectSelectedContainer(index int, cli *client.Client) {
	if index == -1 {
		return
	}
	containers, err := cli.ContainerList(context.Background(), dockerContainer.ListOptions{All: true})
	if err != nil || index >= len(containers) {
		return
	}
	info, err := cli.ContainerInspect(context.Background(), containers[index].ID)
	if err != nil {
		log.Println("Error inspecting container:", err)
		return
	}
	content := fmt.Sprintf("ID: %s\nImage: %s\nCmd: %v\nState: %v\n", info.ID, info.Image, info.Config.Cmd, info.State)
	win := appInstance.NewWindow("Inspect Container")
	win.SetContent(container.NewScroll(widget.NewLabel(content)))
	win.Resize(fyne.NewSize(600, 400))
	win.Show()
}

func runAlpineContainer(cli *client.Client, data *[]string, list *widget.List) {
	image := "alpine"
	cmd := []string{"echo", "Hello from Alpine!"}
	if _, err := cli.ImagePull(context.Background(), image, dockerImage.PullOptions{}); err != nil {
		log.Println("Error pulling Alpine image:", err)
		return
	}
	resp, err := cli.ContainerCreate(context.Background(), &dockerContainer.Config{Image: image, Cmd: cmd}, &dockerContainer.HostConfig{}, nil, nil, "")
	if err != nil {
		log.Println("Error creating Alpine container:", err)
		return
	}
	if err = cli.ContainerStart(context.Background(), resp.ID, dockerContainer.StartOptions{}); err != nil {
		log.Println("Error starting Alpine container:", err)
		return
	}
	updateContainerList(data, list, cli)
}

func showCustomContainerForm(cli *client.Client, data *[]string, list *widget.List) {
	win := appInstance.NewWindow("Run Custom Container")
	win.Resize(fyne.NewSize(400, 300))
	imageEntry := widget.NewEntry()
	imageEntry.SetText("alpine")
	cmdEntry := widget.NewEntry()
	cmdEntry.SetText("echo hello world")
	envEntry := widget.NewEntry()
	envEntry.SetText("KEY=value,FOO=bar")
	portsEntry := widget.NewEntry()
	portsEntry.SetText("8080:80")
	form := widget.NewForm(
		widget.NewFormItem("Image", imageEntry),
		widget.NewFormItem("Command", cmdEntry),
		widget.NewFormItem("Env (comma-separated)", envEntry),
		widget.NewFormItem("Ports (comma-separated, e.g. 8080:80)", portsEntry),
	)
	form.OnSubmit = func() {
		image := imageEntry.Text
		cmdParts := strings.Fields(cmdEntry.Text)
		var envVars []string
		if envEntry.Text != "" {
			envVars = strings.Split(envEntry.Text, ",")
		}
		portBindings := nat.PortMap{}
		if portsEntry.Text != "" {
			pairs := strings.Split(portsEntry.Text, ",")
			for _, p := range pairs {
				p = strings.TrimSpace(p)
				parts := strings.Split(p, ":")
				if len(parts) == 2 {
					hostPort := parts[0]
					containerPort := parts[1]
					port := nat.Port(containerPort + "/tcp")
					portBindings[port] = []nat.PortBinding{{HostIP: "", HostPort: hostPort}}
				}
			}
		}
		if _, err := cli.ImagePull(context.Background(), image, dockerImage.PullOptions{}); err != nil {
			dialog.ShowError(err, win)
			return
		}
		resp, err := cli.ContainerCreate(context.Background(),
			&dockerContainer.Config{
				Image: image,
				Cmd:   cmdParts,
				Env:   envVars,
			},
			&dockerContainer.HostConfig{PortBindings: portBindings},
			nil, nil, "",
		)
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		if err := cli.ContainerStart(context.Background(), resp.ID, dockerContainer.StartOptions{}); err != nil {
			dialog.ShowError(err, win)
			return
		}
		updateContainerList(data, list, cli)
		win.Close()
	}
	win.SetContent(form)
	win.Show()
}

// =============================================================================
// Images Tab
// =============================================================================

func buildImagesTab(cli *client.Client) fyne.CanvasObject {
	var imagesData []string
	imagesList := widget.NewList(
		func() int { return len(imagesData) },
		func() fyne.CanvasObject {
			lbl := widget.NewLabel("")
			lbl.Wrapping = fyne.TextWrapWord
			return lbl
		},
		func(i int, obj fyne.CanvasObject) {
			obj.(*widget.Label).SetText(imagesData[i])
		},
	)
	imagesList.OnSelected = func(id int) {
		selectedImageIndex = id
		fmt.Println("Selected image:", imagesData[id])
	}
	scrollableImagesList := container.NewScroll(imagesList)
	scrollableImagesList.SetMinSize(fyne.NewSize(1000, 500))
	refreshBtn := widget.NewButton("Refresh", func() {
		updateImagesList(&imagesData, imagesList, cli)
	})
	pullBtn := widget.NewButton("Pull Image", func() {
		showPullImageDialog(cli, &imagesData, imagesList)
	})
	removeBtn := widget.NewButton("Remove Image", func() {
		removeSelectedImage(selectedImageIndex, cli, &imagesData, imagesList)
	})
	topRow := container.NewHBox(refreshBtn, pullBtn, removeBtn)
	box := container.NewVBox(scrollableImagesList, topRow)
	updateImagesList(&imagesData, imagesList, cli)
	return box
}

func updateImagesList(data *[]string, list *widget.List, cli *client.Client) {
	images, err := cli.ImageList(context.Background(), dockerImage.ListOptions{})
	if err != nil {
		log.Println("Error fetching images:", err)
		return
	}
	*data = make([]string, len(images))
	for i, img := range images {
		shortID := ""
		if len(img.ID) > 12 {
			shortID = img.ID[7:19]
		}
		(*data)[i] = fmt.Sprintf("ID:%s | Tags:%v | Size:%d", shortID, img.RepoTags, img.Size)
	}
	list.Refresh()
}

func showPullImageDialog(cli *client.Client, data *[]string, list *widget.List) {
	win := appInstance.NewWindow("Pull Image")
	entry := widget.NewEntry()
	entry.SetText("alpine")
	form := widget.NewForm(
		widget.NewFormItem("Image Name (e.g. alpine:latest)", entry),
	)
	form.OnSubmit = func() {
		imageName := entry.Text
		_, err := cli.ImagePull(context.Background(), imageName, dockerImage.PullOptions{})
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		updateImagesList(data, list, cli)
		win.Close()
	}
	win.SetContent(form)
	win.Resize(fyne.NewSize(300, 150))
	win.Show()
}

func removeSelectedImage(index int, cli *client.Client, data *[]string, list *widget.List) {
	if index == -1 {
		return
	}
	images, err := cli.ImageList(context.Background(), dockerImage.ListOptions{})
	if err != nil || index >= len(images) {
		return
	}
	_, err = cli.ImageRemove(context.Background(), images[index].ID, dockerImage.RemoveOptions{Force: true})
	if err != nil {
		log.Println("Error removing image:", err)
		return
	}
	updateImagesList(data, list, cli)
}

// =============================================================================
// Volumes Tab
// =============================================================================

func buildVolumesTab(cli *client.Client) fyne.CanvasObject {
	var volumesData []string
	volumesList := widget.NewList(
		func() int { return len(volumesData) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i int, obj fyne.CanvasObject) {
			obj.(*widget.Label).SetText(volumesData[i])
		},
	)
	volumesList.OnSelected = func(id int) {
		selectedVolumeIndex = id
		fmt.Println("Selected volume:", volumesData[id])
	}
	refreshBtn := widget.NewButton("Refresh", func() {
		updateVolumesList(&volumesData, volumesList, cli)
	})
	createBtn := widget.NewButton("Create Volume", func() {
		showCreateVolumeDialog(cli, &volumesData, volumesList)
	})
	removeBtn := widget.NewButton("Remove Volume", func() {
		removeSelectedVolume(selectedVolumeIndex, cli, &volumesData, volumesList)
	})
	scrollableVolumesList := container.NewScroll(volumesList)
	scrollableVolumesList.SetMinSize(fyne.NewSize(1000, 500))
	topRow := container.NewHBox(refreshBtn, createBtn, removeBtn)
	box := container.NewVBox(scrollableVolumesList, topRow)
	updateVolumesList(&volumesData, volumesList, cli)
	return box
}

func updateVolumesList(data *[]string, list *widget.List, cli *client.Client) {
	volList, err := cli.VolumeList(context.Background(), volume.ListOptions{Filters: filters.NewArgs()})
	if err != nil {
		log.Println("Error fetching volumes:", err)
		return
	}
	*data = make([]string, len(volList.Volumes))
	for i, v := range volList.Volumes {
		(*data)[i] = fmt.Sprintf("Name:%s | Driver:%s | Mountpoint:%s", v.Name, v.Driver, v.Mountpoint)
	}
	list.Refresh()
}

func showCreateVolumeDialog(cli *client.Client, data *[]string, list *widget.List) {
	win := appInstance.NewWindow("Create Volume")
	nameEntry := widget.NewEntry()
	form := widget.NewForm(
		widget.NewFormItem("Volume Name", nameEntry),
	)
	form.OnSubmit = func() {
		volName := nameEntry.Text
		_, err := cli.VolumeCreate(context.Background(), volume.CreateOptions{Name: volName})
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		updateVolumesList(data, list, cli)
		win.Close()
	}
	win.SetContent(form)
	win.Resize(fyne.NewSize(300, 150))
	win.Show()
}

func removeSelectedVolume(index int, cli *client.Client, data *[]string, list *widget.List) {
	if index == -1 {
		return
	}
	volList, err := cli.VolumeList(context.Background(), volume.ListOptions{Filters: filters.NewArgs()})
	if err != nil || index >= len(volList.Volumes) {
		return
	}
	volName := volList.Volumes[index].Name
	if err = cli.VolumeRemove(context.Background(), volName, true); err != nil {
		log.Println("Error removing volume:", err)
		return
	}
	updateVolumesList(data, list, cli)
}

// =============================================================================
// Networks Tab
// =============================================================================

func buildNetworksTab(cli *client.Client) fyne.CanvasObject {
	var networksData []string
	networksList := widget.NewList(
		func() int { return len(networksData) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i int, obj fyne.CanvasObject) {
			obj.(*widget.Label).SetText(networksData[i])
		},
	)
	networksList.OnSelected = func(id int) {
		selectedNetworkIndex = id
		fmt.Println("Selected network:", networksData[id])
	}
	refreshBtn := widget.NewButton("Refresh", func() {
		updateNetworksList(&networksData, networksList, cli)
	})
	createBtn := widget.NewButton("Create Network", func() {
		showCreateNetworkDialog(cli, &networksData, networksList)
	})
	removeBtn := widget.NewButton("Remove Network", func() {
		removeSelectedNetwork(selectedNetworkIndex, cli, &networksData, networksList)
	})
	scrollableNetworksList := container.NewScroll(networksList)
	scrollableNetworksList.SetMinSize(fyne.NewSize(1000, 500))
	topRow := container.NewHBox(refreshBtn, createBtn, removeBtn)
	box := container.NewVBox(scrollableNetworksList, topRow)
	updateNetworksList(&networksData, networksList, cli)
	return box
}

func updateNetworksList(data *[]string, list *widget.List, cli *client.Client) {
	nets, err := cli.NetworkList(context.Background(), types.NetworkListOptions{})
	if err != nil {
		log.Println("Error fetching networks:", err)
		return
	}
	*data = make([]string, len(nets))
	for i, net := range nets {
		(*data)[i] = fmt.Sprintf("Name:%s | ID:%s | Scope:%s | Driver:%s", net.Name, net.ID[:12], net.Scope, net.Driver)
	}
	list.Refresh()
}

func showCreateNetworkDialog(cli *client.Client, data *[]string, list *widget.List) {
	win := appInstance.NewWindow("Create Network")
	nameEntry := widget.NewEntry()
	driverEntry := widget.NewEntry()
	driverEntry.SetText("bridge")
	macvlanEntry := widget.NewEntry()
	macvlanEntry.SetPlaceHolder("Optional: macvlan parent (e.g. eth0)")

	form := widget.NewForm(
		widget.NewFormItem("Network Name", nameEntry),
		widget.NewFormItem("Driver", driverEntry),
		widget.NewFormItem("Macvlan Parent", macvlanEntry),
	)
	form.OnSubmit = func() {
		netName := nameEntry.Text
		driver := driverEntry.Text
		options := make(map[string]string)
		if driver == "macvlan" && macvlanEntry.Text != "" {
			options["parent"] = macvlanEntry.Text
		}
		resp, err := cli.NetworkCreate(context.Background(), netName, dockerNetwork.CreateOptions{
			Driver:  driver,
			Options: options,
		})
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		fmt.Println("Created network:", resp.ID)
		updateNetworksList(data, list, cli)
		win.Close()
	}
	form.OnCancel = func() { win.Close() }
	win.SetContent(form)
	win.Resize(fyne.NewSize(400, 250))
	win.Show()
}

func removeSelectedNetwork(index int, cli *client.Client, data *[]string, list *widget.List) {
	if index == -1 {
		return
	}
	nets, err := cli.NetworkList(context.Background(), types.NetworkListOptions{})
	if err != nil || index >= len(nets) {
		return
	}
	netID := nets[index].ID
	if err = cli.NetworkRemove(context.Background(), netID); err != nil {
		log.Println("Error removing network:", err)
		return
	}
	updateNetworksList(data, list, cli)
}
