--
-- PostgreSQL database dump
--

-- Dumped from database version 17.11 (Debian 17.11-1.pgdg12+2)
-- Dumped by pg_dump version 17.11 (Debian 17.11-1.pgdg12+2)

--
-- Name: ai_row_cache; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ai_row_cache (
    id integer NOT NULL,
    generated_at timestamp with time zone NOT NULL,
    payload_json text NOT NULL,
    user_id integer NOT NULL,
    model character varying(100)
);

--
-- Name: ai_row_cache_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.ai_row_cache_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: ai_row_cache_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.ai_row_cache_id_seq OWNED BY public.ai_row_cache.id;

--
-- Name: app_codes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.app_codes (
    code_hash character varying(64) NOT NULL,
    user_id integer NOT NULL,
    challenge character varying(128) NOT NULL,
    device text NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

--
-- Name: app_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.app_settings (
    key character varying(100) NOT NULL,
    value text NOT NULL
);

--
-- Name: app_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.app_tokens (
    id integer NOT NULL,
    user_id integer NOT NULL,
    name text NOT NULL,
    token_hash character varying(64) NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone,
    scope character varying(16) DEFAULT 'app'::character varying NOT NULL
);

--
-- Name: app_tokens_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.app_tokens_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: app_tokens_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.app_tokens_id_seq OWNED BY public.app_tokens.id;

--
-- Name: digests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.digests (
    id integer NOT NULL,
    user_id integer NOT NULL,
    kind character varying(10) NOT NULL,
    period_start timestamp with time zone NOT NULL,
    period_end timestamp with time zone NOT NULL,
    subject character varying(300) NOT NULL,
    html text NOT NULL,
    data_json text NOT NULL,
    model character varying(100),
    created_at timestamp with time zone NOT NULL,
    sent_at timestamp with time zone,
    sent_to character varying(320),
    send_tries integer DEFAULT 0 NOT NULL,
    sending_since timestamp with time zone
);

--
-- Name: digests_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.digests_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: digests_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.digests_id_seq OWNED BY public.digests.id;

--
-- Name: episode_favorites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.episode_favorites (
    id integer NOT NULL,
    user_id integer NOT NULL,
    tmdb_id integer NOT NULL,
    season integer NOT NULL,
    episode integer NOT NULL,
    added_at timestamp with time zone NOT NULL,
    source character varying(30) DEFAULT 'app'::character varying NOT NULL
);

--
-- Name: episode_favorites_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.episode_favorites_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: episode_favorites_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.episode_favorites_id_seq OWNED BY public.episode_favorites.id;

--
-- Name: episode_ratings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.episode_ratings (
    id integer NOT NULL,
    user_id integer NOT NULL,
    tmdb_id integer NOT NULL,
    season integer NOT NULL,
    episode integer NOT NULL,
    rating integer NOT NULL,
    rated_at timestamp with time zone NOT NULL,
    source character varying(30) DEFAULT 'app'::character varying NOT NULL
);

--
-- Name: episode_ratings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.episode_ratings_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: episode_ratings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.episode_ratings_id_seq OWNED BY public.episode_ratings.id;

--
-- Name: favorites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.favorites (
    id integer NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer NOT NULL,
    added_at timestamp with time zone NOT NULL,
    source character varying(30) NOT NULL,
    removed_at timestamp with time zone,
    user_id integer NOT NULL
);

--
-- Name: favorites_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.favorites_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: favorites_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.favorites_id_seq OWNED BY public.favorites.id;

--
-- Name: history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.history (
    id integer NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer,
    season integer,
    episode integer,
    episode_title text,
    title text NOT NULL,
    watched_at timestamp with time zone NOT NULL,
    source character varying(30) NOT NULL,
    source_ref character varying(100),
    user_id integer NOT NULL
);

--
-- Name: history_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.history_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: history_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.history_id_seq OWNED BY public.history.id;

--
-- Name: imdb_ratings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.imdb_ratings (
    tconst character varying(12) NOT NULL,
    rating double precision NOT NULL,
    votes integer NOT NULL
);

--
-- Name: library_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.library_items (
    jellyfin_id character varying(64) NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer,
    title text NOT NULL,
    year integer,
    date_added timestamp with time zone,
    synced_at timestamp with time zone NOT NULL
);

--
-- Name: list_favorites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.list_favorites (
    user_id integer NOT NULL,
    list_id integer NOT NULL,
    added_at timestamp with time zone NOT NULL
);

--
-- Name: list_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.list_items (
    id integer NOT NULL,
    list_id integer NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer NOT NULL,
    "position" integer NOT NULL,
    added_at timestamp with time zone NOT NULL
);

--
-- Name: list_items_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.list_items_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: list_items_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.list_items_id_seq OWNED BY public.list_items.id;

--
-- Name: lists; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.lists (
    id integer NOT NULL,
    name text NOT NULL,
    description text,
    kind character varying(20) NOT NULL,
    source character varying(20) NOT NULL,
    likes integer,
    filters_json text,
    created_at timestamp with time zone NOT NULL,
    synced_at timestamp with time zone,
    owner_id integer,
    origin character varying(20),
    origin_id character varying(64),
    origin_user text,
    origin_slug text,
    not_shown integer
);

--
-- Name: lists_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.lists_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: lists_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.lists_id_seq OWNED BY public.lists.id;

--
-- Name: pair_codes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pair_codes (
    code_hash character varying(64) NOT NULL,
    poll_hash character varying(64) NOT NULL,
    device text NOT NULL,
    user_id integer,
    token_enc text,
    expires_at timestamp with time zone NOT NULL
);

--
-- Name: play_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.play_sessions (
    id character varying(40) NOT NULL,
    user_id integer NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer,
    season integer,
    episode integer,
    title text NOT NULL,
    jellyfin_item_id character varying(64) NOT NULL,
    player character varying(40) NOT NULL,
    started_at timestamp with time zone NOT NULL,
    finished_at timestamp with time zone,
    outcome character varying(20),
    asked_at timestamp with time zone
);

--
-- Name: requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.requests (
    id integer NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer NOT NULL,
    seerr_request_id integer,
    status character varying(30) NOT NULL,
    dry_run boolean NOT NULL,
    created_at timestamp with time zone NOT NULL,
    user_id integer NOT NULL
);

--
-- Name: requests_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.requests_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: requests_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.requests_id_seq OWNED BY public.requests.id;

--
-- Name: title_extras; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.title_extras (
    key character varying(40) NOT NULL,
    imdb_rating double precision,
    imdb_votes integer,
    rt_critics integer,
    rt_audience integer,
    rt_url character varying(300),
    trakt_rating double precision,
    trakt_votes integer,
    wiki_title character varying(300),
    wiki_extract text,
    fetched_at timestamp with time zone NOT NULL,
    metacritic integer,
    letterboxd double precision,
    mdblist_score integer
);

--
-- Name: titles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.titles (
    key character varying(40) NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer NOT NULL,
    title text NOT NULL,
    original_title text,
    year integer,
    release_date character varying(20),
    poster_path character varying(200),
    backdrop_path character varying(200),
    overview text,
    genres_json text,
    runtime integer,
    vote_average double precision,
    vote_count integer,
    imdb_id character varying(20),
    detailed boolean NOT NULL,
    fetched_at timestamp with time zone NOT NULL
);

--
-- Name: user_ratings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_ratings (
    id integer NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer NOT NULL,
    rating integer NOT NULL,
    review text,
    rated_at timestamp with time zone NOT NULL,
    source character varying(30) NOT NULL,
    user_id integer NOT NULL
);

--
-- Name: user_ratings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.user_ratings_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: user_ratings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.user_ratings_id_seq OWNED BY public.user_ratings.id;

--
-- Name: user_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_settings (
    user_id integer NOT NULL,
    key character varying(100) NOT NULL,
    value text NOT NULL
);

--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id integer NOT NULL,
    username character varying(200) NOT NULL,
    email character varying(320),
    display_name text,
    is_admin boolean NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone,
    jellyfin_user_id character varying(64),
    jellyfin_user_name character varying(200),
    jellyfin_token_enc text,
    jellyfin_linked_at timestamp with time zone,
    seerr_user_id integer,
    scrobble_token_hash character varying(64),
    oidc_sub character varying(255),
    password_hash text,
    session_epoch integer DEFAULT 0 NOT NULL
);

--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;

--
-- Name: watch_progress; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.watch_progress (
    id integer NOT NULL,
    user_id integer NOT NULL,
    key character varying(60) NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer NOT NULL,
    season integer,
    episode integer,
    title text DEFAULT ''::character varying NOT NULL,
    episode_title text,
    position_s integer DEFAULT 0 NOT NULL,
    duration_s integer,
    state character varying(10) DEFAULT 'playing'::character varying NOT NULL,
    client text,
    device text,
    jellyfin_item_id character varying(64),
    updated_at timestamp with time zone NOT NULL
);

--
-- Name: watch_progress_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.watch_progress_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: watch_progress_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.watch_progress_id_seq OWNED BY public.watch_progress.id;

--
-- Name: watchlist; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.watchlist (
    id integer NOT NULL,
    media_type character varying(10) NOT NULL,
    tmdb_id integer NOT NULL,
    added_at timestamp with time zone NOT NULL,
    source character varying(30) NOT NULL,
    removed_at timestamp with time zone,
    user_id integer NOT NULL
);

--
-- Name: watchlist_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.watchlist_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: watchlist_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.watchlist_id_seq OWNED BY public.watchlist.id;

--
-- Name: ai_row_cache id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_row_cache ALTER COLUMN id SET DEFAULT nextval('public.ai_row_cache_id_seq'::regclass);

--
-- Name: app_tokens id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_tokens ALTER COLUMN id SET DEFAULT nextval('public.app_tokens_id_seq'::regclass);

--
-- Name: digests id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.digests ALTER COLUMN id SET DEFAULT nextval('public.digests_id_seq'::regclass);

--
-- Name: episode_favorites id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episode_favorites ALTER COLUMN id SET DEFAULT nextval('public.episode_favorites_id_seq'::regclass);

--
-- Name: episode_ratings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episode_ratings ALTER COLUMN id SET DEFAULT nextval('public.episode_ratings_id_seq'::regclass);

--
-- Name: favorites id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.favorites ALTER COLUMN id SET DEFAULT nextval('public.favorites_id_seq'::regclass);

--
-- Name: history id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.history ALTER COLUMN id SET DEFAULT nextval('public.history_id_seq'::regclass);

--
-- Name: list_items id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.list_items ALTER COLUMN id SET DEFAULT nextval('public.list_items_id_seq'::regclass);

--
-- Name: lists id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lists ALTER COLUMN id SET DEFAULT nextval('public.lists_id_seq'::regclass);

--
-- Name: requests id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.requests ALTER COLUMN id SET DEFAULT nextval('public.requests_id_seq'::regclass);

--
-- Name: user_ratings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_ratings ALTER COLUMN id SET DEFAULT nextval('public.user_ratings_id_seq'::regclass);

--
-- Name: users id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);

--
-- Name: watch_progress id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watch_progress ALTER COLUMN id SET DEFAULT nextval('public.watch_progress_id_seq'::regclass);

--
-- Name: watchlist id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watchlist ALTER COLUMN id SET DEFAULT nextval('public.watchlist_id_seq'::regclass);

--
-- Name: ai_row_cache ai_row_cache_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_row_cache
    ADD CONSTRAINT ai_row_cache_pkey PRIMARY KEY (id);

--
-- Name: app_codes app_codes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_codes
    ADD CONSTRAINT app_codes_pkey PRIMARY KEY (code_hash);

--
-- Name: app_settings app_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_settings
    ADD CONSTRAINT app_settings_pkey PRIMARY KEY (key);

--
-- Name: app_tokens app_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_tokens
    ADD CONSTRAINT app_tokens_pkey PRIMARY KEY (id);

--
-- Name: app_tokens app_tokens_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_tokens
    ADD CONSTRAINT app_tokens_token_hash_key UNIQUE (token_hash);

--
-- Name: digests digests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.digests
    ADD CONSTRAINT digests_pkey PRIMARY KEY (id);

--
-- Name: episode_favorites episode_favorites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episode_favorites
    ADD CONSTRAINT episode_favorites_pkey PRIMARY KEY (id);

--
-- Name: episode_ratings episode_ratings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episode_ratings
    ADD CONSTRAINT episode_ratings_pkey PRIMARY KEY (id);

--
-- Name: favorites favorites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.favorites
    ADD CONSTRAINT favorites_pkey PRIMARY KEY (id);

--
-- Name: history history_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.history
    ADD CONSTRAINT history_pkey PRIMARY KEY (id);

--
-- Name: imdb_ratings imdb_ratings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.imdb_ratings
    ADD CONSTRAINT imdb_ratings_pkey PRIMARY KEY (tconst);

--
-- Name: library_items library_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_items
    ADD CONSTRAINT library_items_pkey PRIMARY KEY (jellyfin_id);

--
-- Name: list_favorites list_favorites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.list_favorites
    ADD CONSTRAINT list_favorites_pkey PRIMARY KEY (user_id, list_id);

--
-- Name: list_items list_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.list_items
    ADD CONSTRAINT list_items_pkey PRIMARY KEY (id);

--
-- Name: lists lists_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lists
    ADD CONSTRAINT lists_pkey PRIMARY KEY (id);

--
-- Name: pair_codes pair_codes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pair_codes
    ADD CONSTRAINT pair_codes_pkey PRIMARY KEY (code_hash);

--
-- Name: pair_codes pair_codes_poll_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pair_codes
    ADD CONSTRAINT pair_codes_poll_hash_key UNIQUE (poll_hash);

--
-- Name: play_sessions play_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.play_sessions
    ADD CONSTRAINT play_sessions_pkey PRIMARY KEY (id);

--
-- Name: requests requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.requests
    ADD CONSTRAINT requests_pkey PRIMARY KEY (id);

--
-- Name: title_extras title_extras_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.title_extras
    ADD CONSTRAINT title_extras_pkey PRIMARY KEY (key);

--
-- Name: titles titles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.titles
    ADD CONSTRAINT titles_pkey PRIMARY KEY (key);

--
-- Name: digests uq_digest_period; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.digests
    ADD CONSTRAINT uq_digest_period UNIQUE (user_id, kind, period_start);

--
-- Name: episode_favorites uq_episode_favorite; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episode_favorites
    ADD CONSTRAINT uq_episode_favorite UNIQUE (user_id, tmdb_id, season, episode);

--
-- Name: episode_ratings uq_episode_rating; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episode_ratings
    ADD CONSTRAINT uq_episode_rating UNIQUE (user_id, tmdb_id, season, episode);

--
-- Name: favorites uq_favorite_title; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.favorites
    ADD CONSTRAINT uq_favorite_title UNIQUE (user_id, media_type, tmdb_id);

--
-- Name: history uq_history_source_ref; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.history
    ADD CONSTRAINT uq_history_source_ref UNIQUE (user_id, source, source_ref);

--
-- Name: list_items uq_list_item; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.list_items
    ADD CONSTRAINT uq_list_item UNIQUE (list_id, media_type, tmdb_id);

--
-- Name: user_ratings uq_rating_title; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_ratings
    ADD CONSTRAINT uq_rating_title UNIQUE (user_id, media_type, tmdb_id);

--
-- Name: users uq_users_oidc_sub; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT uq_users_oidc_sub UNIQUE (oidc_sub);

--
-- Name: watch_progress uq_watch_progress; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watch_progress
    ADD CONSTRAINT uq_watch_progress UNIQUE (user_id, key);

--
-- Name: watchlist uq_watchlist_title; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watchlist
    ADD CONSTRAINT uq_watchlist_title UNIQUE (user_id, media_type, tmdb_id);

--
-- Name: user_ratings user_ratings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_ratings
    ADD CONSTRAINT user_ratings_pkey PRIMARY KEY (id);

--
-- Name: user_settings user_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_settings
    ADD CONSTRAINT user_settings_pkey PRIMARY KEY (user_id, key);

--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

--
-- Name: users users_scrobble_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_scrobble_token_hash_key UNIQUE (scrobble_token_hash);

--
-- Name: users users_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_username_key UNIQUE (username);

--
-- Name: watch_progress watch_progress_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watch_progress
    ADD CONSTRAINT watch_progress_pkey PRIMARY KEY (id);

--
-- Name: watchlist watchlist_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watchlist
    ADD CONSTRAINT watchlist_pkey PRIMARY KEY (id);

--
-- Name: ix_ai_row_cache_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_ai_row_cache_user_id ON public.ai_row_cache USING btree (user_id);

--
-- Name: ix_app_tokens_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_app_tokens_user_id ON public.app_tokens USING btree (user_id);

--
-- Name: ix_digests_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_digests_user_id ON public.digests USING btree (user_id);

--
-- Name: ix_episode_favorites_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_episode_favorites_user_id ON public.episode_favorites USING btree (user_id);

--
-- Name: ix_episode_ratings_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_episode_ratings_user_id ON public.episode_ratings USING btree (user_id);

--
-- Name: ix_favorites_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_favorites_tmdb_id ON public.favorites USING btree (tmdb_id);

--
-- Name: ix_favorites_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_favorites_user_id ON public.favorites USING btree (user_id);

--
-- Name: ix_history_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_history_tmdb_id ON public.history USING btree (tmdb_id);

--
-- Name: ix_history_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_history_user_id ON public.history USING btree (user_id);

--
-- Name: ix_history_user_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_history_user_time ON public.history USING btree (user_id, watched_at);

--
-- Name: ix_history_watched_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_history_watched_at ON public.history USING btree (watched_at);

--
-- Name: ix_library_items_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_library_items_tmdb_id ON public.library_items USING btree (tmdb_id);

--
-- Name: ix_list_items_list_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_list_items_list_id ON public.list_items USING btree (list_id);

--
-- Name: ix_list_items_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_list_items_tmdb_id ON public.list_items USING btree (tmdb_id);

--
-- Name: ix_lists_origin_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_lists_origin_id ON public.lists USING btree (origin_id);

--
-- Name: ix_lists_owner_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_lists_owner_id ON public.lists USING btree (owner_id);

--
-- Name: ix_play_sessions_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_play_sessions_user_id ON public.play_sessions USING btree (user_id);

--
-- Name: ix_requests_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_requests_tmdb_id ON public.requests USING btree (tmdb_id);

--
-- Name: ix_requests_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_requests_user_id ON public.requests USING btree (user_id);

--
-- Name: ix_titles_imdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_titles_imdb_id ON public.titles USING btree (imdb_id);

--
-- Name: ix_titles_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_titles_tmdb_id ON public.titles USING btree (tmdb_id);

--
-- Name: ix_user_ratings_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_user_ratings_tmdb_id ON public.user_ratings USING btree (tmdb_id);

--
-- Name: ix_user_ratings_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_user_ratings_user_id ON public.user_ratings USING btree (user_id);

--
-- Name: ix_users_jellyfin_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_users_jellyfin_user_id ON public.users USING btree (jellyfin_user_id);

--
-- Name: ix_watch_progress_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_watch_progress_user_id ON public.watch_progress USING btree (user_id);

--
-- Name: ix_watchlist_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_watchlist_tmdb_id ON public.watchlist USING btree (tmdb_id);

--
-- Name: ix_watchlist_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_watchlist_user_id ON public.watchlist USING btree (user_id);

--
-- Name: app_codes app_codes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_codes
    ADD CONSTRAINT app_codes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: app_tokens app_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_tokens
    ADD CONSTRAINT app_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: digests digests_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.digests
    ADD CONSTRAINT digests_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: episode_favorites episode_favorites_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episode_favorites
    ADD CONSTRAINT episode_favorites_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: episode_ratings episode_ratings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episode_ratings
    ADD CONSTRAINT episode_ratings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: ai_row_cache fk_ai_row_cache_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_row_cache
    ADD CONSTRAINT fk_ai_row_cache_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: favorites fk_favorites_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.favorites
    ADD CONSTRAINT fk_favorites_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: history fk_history_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.history
    ADD CONSTRAINT fk_history_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: lists fk_lists_owner; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lists
    ADD CONSTRAINT fk_lists_owner FOREIGN KEY (owner_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: requests fk_requests_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.requests
    ADD CONSTRAINT fk_requests_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_ratings fk_user_ratings_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_ratings
    ADD CONSTRAINT fk_user_ratings_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: watchlist fk_watchlist_user; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watchlist
    ADD CONSTRAINT fk_watchlist_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: list_favorites list_favorites_list_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.list_favorites
    ADD CONSTRAINT list_favorites_list_id_fkey FOREIGN KEY (list_id) REFERENCES public.lists(id) ON DELETE CASCADE;

--
-- Name: list_favorites list_favorites_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.list_favorites
    ADD CONSTRAINT list_favorites_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: list_items list_items_list_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.list_items
    ADD CONSTRAINT list_items_list_id_fkey FOREIGN KEY (list_id) REFERENCES public.lists(id) ON DELETE CASCADE;

--
-- Name: pair_codes pair_codes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pair_codes
    ADD CONSTRAINT pair_codes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: play_sessions play_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.play_sessions
    ADD CONSTRAINT play_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_settings user_settings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_settings
    ADD CONSTRAINT user_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: watch_progress watch_progress_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watch_progress
    ADD CONSTRAINT watch_progress_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- PostgreSQL database dump complete
--

