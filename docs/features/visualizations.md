# Visualizations

A visualization replaces the tabs and editor in the center pane with another view of the repository you have checked out. Click the skyscraper button in the sidebar footer (left of GitHub) and pick one from the menu. Pick it again, or choose **Back to editor** in the Controls menu, to return to your tabs.

Visualizations are always for the **currently checked-out branch**. They are deterministic: no LLM, agent, or harness is involved.

## Build, cache, refresh

- The first time you open a visualization on a branch, px0 builds it (a second or two for most repositories).
- The result is cached in the px0 cache directory (`$XDG_CACHE_HOME/px0/viz/`, or `~/.px0/cache/viz/`), never inside your workspace, so reopening is instant, also after a restart.
- Each branch has its own cached model. Switching branches in a terminal and returning to the window shows the new branch's model.
- The bar at the top shows the branch and an **out of date** badge when new commits landed after the model was built.
- **Controls → Refresh** rebuilds from the code as it is now, including uncommitted changes.

## CodeCity

Directories become districts and files become buildings (after Richard Wettel's CodeCity).

| Property | Metric |
| :--- | :--- |
| Building height | Number of methods/functions, binned so one outlier cannot flatten the skyline |
| Building footprint | Number of fields (lines of code when there are none) |
| Building color | Lines of code, gray to blue |
| District shade | Nesting depth |
| Gold mast | Likely entry point (`main`, `index`, `app`, `server`, ...) |

Method and field counts are regex heuristics, so treat them as approximate. Disharmony coloring (god, brain, data class, antenna) marks candidates; generated code is a common false positive.

Controls: drag to orbit; pan with the hand button (or `H`), right-drag or Shift-drag; pinch (trackpad or touch), scroll, `+`/`-` or the Zoom buttons at the bottom right to zoom; `W A S D` to fly, `Q`/`E` for height, `F` to focus the selection, `Esc` to clear it. Click a building to inspect it, and use **Open in editor** to jump to the file. The Controls menu switches between size and disharmony coloring, overview and street camera, and district labels.

The 3D engine (three.js) is downloaded once on first use and cached, like the Mermaid renderer.
