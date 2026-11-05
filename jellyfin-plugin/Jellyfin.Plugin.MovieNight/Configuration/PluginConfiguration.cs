using MediaBrowser.Model.Plugins;

namespace Jellyfin.Plugin.MovieNight.Configuration;

public class PluginConfiguration : BasePluginConfiguration
{
    /// <summary>Movie Night's address, e.g. https://movienight.example.com.</summary>
    public string MovieNightUrl { get; set; } = string.Empty;

    /// <summary>Movie Night's JELLYFIN_WEBHOOK_SECRET.</summary>
    public string Token { get; set; } = string.Empty;

    /// <summary>How often a playing title's position is sent, in seconds (default 30;
    /// never more often than every 10 s, and the settings page allows up to 600).
    /// Pause and stop are always sent at once.</summary>
    public int ProgressSeconds { get; set; } = 30;

    /// <summary>Events kept in memory while Movie Night isn't taking them
    /// (default 1,000; at least 1, since a queue needs room for one). When it
    /// is full the oldest goes, and Jellyfin's log says so; plays still reach
    /// Movie Night through its plays sync. Read when Jellyfin starts.</summary>
    public int QueueSize { get; set; } = 1000;
}
