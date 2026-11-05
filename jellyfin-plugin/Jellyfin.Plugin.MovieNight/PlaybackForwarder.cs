using System.Collections.Concurrent;
using System.Net.Http.Json;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Threading.Channels;
using MediaBrowser.Common.Net;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Entities.Movies;
using MediaBrowser.Controller.Entities.TV;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.Session;
using MediaBrowser.Model.Entities;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.MovieNight;

/// <summary>
/// Listens to Jellyfin's playback events (every app that plays through
/// Jellyfin raises them, Kodi's Jellyfin add-on included) and to played /
/// unplayed marks, and sends each to Movie Night's
/// /api/v1/scrobble/jellyfin/events in order. Progress is sent at most every
/// ProgressSeconds per person and title (pause and stop always). A failed
/// send gets 4 attempts in all, 5, 20 and 80 s apart, each with a 20 s
/// timeout, and is then dropped; a 4xx answer (a wrong token, say) is not
/// retried. The queue keeps the latest QueueSize events (1,000 by default, a
/// setting) and drops the oldest. Every drop is logged as a warning, never
/// silent. These limits are listed in Movie Night's docs/limits.md (Jellyfin
/// plugin).
/// </summary>
public sealed class PlaybackForwarder : IHostedService, IDisposable
{
    private static readonly JsonSerializerOptions Json = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
    };

    private readonly ISessionManager _sessions;
    private readonly IUserDataManager _userData;
    private readonly IHttpClientFactory _httpClientFactory;
    private readonly ILogger<PlaybackForwarder> _logger;
    private readonly Channel<PlayEvent> _queue;
    private readonly ConcurrentDictionary<string, (DateTime At, bool Paused)> _lastSent = new();
    private CancellationTokenSource? _stopping;
    private Task? _sender;

    public PlaybackForwarder(ISessionManager sessions, IUserDataManager userData, IHttpClientFactory httpClientFactory,
        ILogger<PlaybackForwarder> logger)
    {
        _sessions = sessions;
        _userData = userData;
        _httpClientFactory = httpClientFactory;
        _logger = logger;
        // The setting as given; a queue needs room for at least one event, so
        // a lower value keeps one, and the log says so (no floor of our own).
        var wanted = Plugin.Instance?.Configuration.QueueSize ?? 1000;
        var size = Math.Max(1, wanted);
        if (wanted < 1)
        {
            _logger.LogWarning("\"Events kept while Movie Night doesn't answer\" is {Wanted}; a queue keeps at least 1, so 1 is kept", wanted);
        }

        _queue = Channel.CreateBounded<PlayEvent>(
            new BoundedChannelOptions(size) { FullMode = BoundedChannelFullMode.DropOldest, SingleReader = true },
            dropped => _logger.LogWarning(
                "Movie Night isn't taking events (the {Size} waiting are the most kept): the oldest, a {Event} event, was dropped; plays still reach Movie Night through its plays sync",
                size, dropped.Event));
    }

    public Task StartAsync(CancellationToken cancellationToken)
    {
        _sessions.PlaybackStart += OnPlaybackStart;
        _sessions.PlaybackProgress += OnPlaybackProgress;
        _sessions.PlaybackStopped += OnPlaybackStopped;
        _userData.UserDataSaved += OnUserDataSaved;
        _stopping = new CancellationTokenSource();
        _sender = Task.Run(() => SendLoop(_stopping.Token), CancellationToken.None);
        return Task.CompletedTask;
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        _sessions.PlaybackStart -= OnPlaybackStart;
        _sessions.PlaybackProgress -= OnPlaybackProgress;
        _sessions.PlaybackStopped -= OnPlaybackStopped;
        _userData.UserDataSaved -= OnUserDataSaved;
        _queue.Writer.TryComplete();
        _stopping?.Cancel();
        if (_sender is not null)
        {
            try
            {
                await _sender.ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
            }
        }
    }

    public void Dispose() => _stopping?.Dispose();

    private void OnPlaybackStart(object? sender, PlaybackProgressEventArgs e) => FromPlayback("start", e, false);

    private void OnPlaybackProgress(object? sender, PlaybackProgressEventArgs e) => FromPlayback(e.IsPaused ? "pause" : "progress", e, false);

    private void OnPlaybackStopped(object? sender, PlaybackStopEventArgs e) => FromPlayback("stop", e, e.PlayedToCompletion);

    private void FromPlayback(string kind, PlaybackProgressEventArgs e, bool playedToCompletion)
    {
        try
        {
            var item = ItemOf(e.Item);
            if (item is null)
            {
                return;
            }

            var every = TimeSpan.FromSeconds(Math.Max(10, Plugin.Instance?.Configuration.ProgressSeconds ?? 30));
            foreach (var user in e.Users)
            {
                var userId = user.Id.ToString("N");
                var key = userId + ":" + item.Id;
                var now = DateTime.UtcNow;
                if (kind is "progress" or "pause")
                {
                    var paused = kind == "pause";
                    if (_lastSent.TryGetValue(key, out var last) && last.Paused == paused && now - last.At < every)
                    {
                        continue;
                    }

                    _lastSent[key] = (now, paused);
                }
                else if (kind == "stop")
                {
                    _lastSent.TryRemove(key, out _);
                }
                else
                {
                    _lastSent[key] = (now, false);
                }

                Enqueue(new PlayEvent
                {
                    Event = kind,
                    UserId = userId,
                    Item = item,
                    PositionS = e.PlaybackPositionTicks is long ticks ? ticks / TimeSpan.TicksPerSecond : null,
                    PlayedToCompletion = playedToCompletion,
                    Client = e.ClientName,
                    Device = e.DeviceName,
                });
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Movie Night: could not read a playback event");
        }
    }

    private void OnUserDataSaved(object? sender, UserDataSaveEventArgs e)
    {
        try
        {
            if (e.SaveReason is not (UserDataSaveReason.PlaybackFinished or UserDataSaveReason.TogglePlayed))
            {
                return;
            }

            var item = ItemOf(e.Item);
            if (item is null || e.UserData is null)
            {
                return;
            }

            // A stop before the end is saved as "finished, not played": that is
            // not someone marking it unplayed, and the stop event already says
            // where it was left.
            if (e.SaveReason == UserDataSaveReason.PlaybackFinished && !e.UserData.Played)
            {
                return;
            }

            Enqueue(new PlayEvent
            {
                Event = e.UserData.Played ? "played" : "unplayed",
                UserId = e.UserId.ToString("N"),
                Item = item,
                LastPlayed = e.UserData.LastPlayedDate?.ToUniversalTime().ToString("o"),
            });
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Movie Night: could not read a played mark");
        }
    }

    private static PlayItem? ItemOf(BaseItem? item)
    {
        switch (item)
        {
            case Movie movie:
                return new PlayItem
                {
                    Id = movie.Id.ToString("N"),
                    Type = "Movie",
                    Name = movie.Name,
                    Year = movie.ProductionYear,
                    RuntimeS = movie.RunTimeTicks / TimeSpan.TicksPerSecond,
                    Tmdb = Provider(movie, "Tmdb"),
                    Imdb = Provider(movie, "Imdb"),
                };
            case Episode episode:
                var series = episode.Series;
                return new PlayItem
                {
                    Id = episode.Id.ToString("N"),
                    Type = "Episode",
                    Name = episode.Name,
                    SeriesId = episode.SeriesId == Guid.Empty ? null : episode.SeriesId.ToString("N"),
                    SeriesName = episode.SeriesName,
                    Season = episode.ParentIndexNumber,
                    Episode = episode.IndexNumber,
                    Year = episode.ProductionYear,
                    RuntimeS = episode.RunTimeTicks / TimeSpan.TicksPerSecond,
                    Tmdb = Provider(episode, "Tmdb"),
                    Imdb = Provider(episode, "Imdb"),
                    Tvdb = Provider(episode, "Tvdb"),
                    SeriesTmdb = series is null ? null : Provider(series, "Tmdb"),
                    SeriesImdb = series is null ? null : Provider(series, "Imdb"),
                    SeriesTvdb = series is null ? null : Provider(series, "Tvdb"),
                };
            default:
                return null;
        }
    }

    private static string? Provider(BaseItem item, string name) =>
        item.ProviderIds is not null && item.ProviderIds.TryGetValue(name, out var value) && !string.IsNullOrWhiteSpace(value)
            ? value
            : null;

    private void Enqueue(PlayEvent ev)
    {
        if (!_queue.Writer.TryWrite(ev))
        {
            _logger.LogDebug("Movie Night: queue closed, event dropped");
        }
    }

    private async Task SendLoop(CancellationToken stopping)
    {
        var http = _httpClientFactory.CreateClient(NamedClient.Default);
        await foreach (var ev in _queue.Reader.ReadAllAsync(stopping).ConfigureAwait(false))
        {
            var config = Plugin.Instance?.Configuration;
            if (config is null || string.IsNullOrWhiteSpace(config.MovieNightUrl) || string.IsNullOrWhiteSpace(config.Token))
            {
                continue;
            }

            var url = config.MovieNightUrl.TrimEnd('/') + "/api/v1/scrobble/jellyfin/events";
            for (var attempt = 0; attempt < 4; attempt++)
            {
                try
                {
                    using var request = new HttpRequestMessage(HttpMethod.Post, url)
                    {
                        Content = JsonContent.Create(ev, options: Json),
                    };
                    request.Headers.Add("X-MovieNight-Token", config.Token);
                    using var timeout = CancellationTokenSource.CreateLinkedTokenSource(stopping);
                    timeout.CancelAfter(TimeSpan.FromSeconds(20));
                    using var response = await http.SendAsync(request, timeout.Token).ConfigureAwait(false);
                    if (response.IsSuccessStatusCode)
                    {
                        break;
                    }

                    if ((int)response.StatusCode is >= 400 and < 500)
                    {
                        _logger.LogWarning("Movie Night refused a {Event} event: HTTP {Status}", ev.Event, (int)response.StatusCode);
                        break;
                    }
                }
                catch (OperationCanceledException) when (stopping.IsCancellationRequested)
                {
                    return;
                }
                catch (Exception ex)
                {
                    _logger.LogDebug(ex, "Movie Night: send failed (attempt {Attempt})", attempt + 1);
                }

                if (attempt < 3)
                {
                    await Task.Delay(TimeSpan.FromSeconds(5 * Math.Pow(4, attempt)), stopping).ConfigureAwait(false);
                }
                else
                {
                    _logger.LogWarning("Movie Night did not answer; a {Event} event was dropped", ev.Event);
                }
            }
        }
    }

    private sealed class PlayEvent
    {
        public string Event { get; set; } = string.Empty;

        public string UserId { get; set; } = string.Empty;

        public PlayItem Item { get; set; } = new();

        public long? PositionS { get; set; }

        public bool PlayedToCompletion { get; set; }

        public string? LastPlayed { get; set; }

        public string? Client { get; set; }

        public string? Device { get; set; }
    }

    private sealed class PlayItem
    {
        public string Id { get; set; } = string.Empty;

        public string Type { get; set; } = string.Empty;

        public string Name { get; set; } = string.Empty;

        public string? SeriesId { get; set; }

        public string? SeriesName { get; set; }

        public int? Season { get; set; }

        public int? Episode { get; set; }

        public int? Year { get; set; }

        public long? RuntimeS { get; set; }

        public string? Tmdb { get; set; }

        public string? Imdb { get; set; }

        public string? Tvdb { get; set; }

        public string? SeriesTmdb { get; set; }

        public string? SeriesImdb { get; set; }

        public string? SeriesTvdb { get; set; }
    }
}
