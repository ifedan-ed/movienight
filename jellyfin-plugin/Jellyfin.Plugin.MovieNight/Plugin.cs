using Jellyfin.Plugin.MovieNight.Configuration;
using MediaBrowser.Common.Configuration;
using MediaBrowser.Common.Plugins;
using MediaBrowser.Model.Plugins;
using MediaBrowser.Model.Serialization;

namespace Jellyfin.Plugin.MovieNight;

/// <summary>
/// Movie Night for Jellyfin: tells Movie Night what each person plays here
/// (start, progress, pause, stop) and what they mark played, so Movie Night
/// keeps their history and where they stopped, whatever app they play in.
/// </summary>
public class Plugin : BasePlugin<PluginConfiguration>, IHasWebPages
{
    public Plugin(IApplicationPaths applicationPaths, IXmlSerializer xmlSerializer)
        : base(applicationPaths, xmlSerializer)
    {
        Instance = this;
    }

    public static Plugin? Instance { get; private set; }

    public override string Name => "Movie Night";

    public override Guid Id => Guid.Parse("5bf59830-f994-4e33-8e31-0b4570f490b2");

    public override string Description => "Sends plays, progress and played marks to Movie Night.";

    public IEnumerable<PluginPageInfo> GetPages() =>
    [
        new PluginPageInfo
        {
            Name = Name,
            EmbeddedResourcePath = $"{GetType().Namespace}.Configuration.configPage.html",
        },
    ];
}
