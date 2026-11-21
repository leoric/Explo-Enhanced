"""Offline checks for the title rules, using real titles from YouTube's Top Weekly Videos Tamil.

Run: python3 test_ytmusic_playlist.py   (needs ytmusicapi installed, makes no network calls)
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ytmusic_playlist import candidates, clean_title, film_of, is_match, resolve, song_name, track_from  # noqa: E402

failed = []


def check(ok):
    """Count instead of stopping at the first failure, so a run shows how many rules hold."""
    check.runs += 1
    if not ok:
        failed.append(sys._getframe(1).f_lineno)


check.runs = 0

# song name = words before the first special character, minus "Video Song"-style words
check(song_name("Kasu Panam Video Song - Extended Version | Soodhu Kavvum | V") == "Kasu Panam")
check(song_name("Bindaas - Lyric Video | Jailer 2| Superstar Rajinikanth | Sun TV") == "Bindaas")
check(song_name("Ennavo Ennavo 8K Song | Priyamaanavale | Thalapathy Vijay") == "Ennavo Ennavo")
check(song_name('Radhimaa [From "Think Indie"] (feat. Sai Smriti)') == "Radhimaa")
check(song_name("Don'u Don'u Don'u (From \"Maari\")") == "Don'u Don'u Don'u")
check(song_name("Pia.. Pia..") == "Pia.. Pia..")
check(song_name("Aura 10/10 (From \"Meesaya Murukku 2\")") == "Aura 10/10")
check(candidates("Love Insurance Kompany - Dheema Promo | Pradeep") == ["Love Insurance Kompany", "Dheema"])

# accept the same song, reject other songs and other versions
check(is_match("Suthudhu Suthudhu Video | Vidyasagar", "Suthudhu Suthudhu", "Suthudhu Suthudhu"))
check(is_match("Vaadi Pottapulla Veliye", "Vaadi Pottapulla Veliye", "Vaadi Pottapulle Veliye"))  # spelling
check(is_match("Hukum", "Hukum", 'Hukum - Thalaivar Alappara - Tamil (From "Jailer")'))
check(not is_match("Raavana Suthudae", "Raavana Suthudae", "Raavana Mavandaa"))
check(not is_match("Rathinamo - Official Music Video", "Rathinamo", "Rathinamo (Female Version)"))
check(not is_match("Koodamela Koodavechi 8K/4K Video Song", "Koodamela Koodavechi", "Koodamela Koodavechi (Rendition)"))
check(not is_match('Tum Tum (From "Enemy - Telugu")', "Tum Tum", 'Tum Tum (From "Enemy - Tamil")'))
check(not is_match("Theri Songs | En Jeevan", "Theri", "Theri Theme"))

# film as album, clean titles keep language
check(film_of('Chola Chola (From "Ponniyin Selvan, Part 1") [Original Motion Picture Soundtrack]') == "Ponniyin Selvan, Part 1")
check(film_of("Annakili (From - Tamizh Kirukkan)") == "Tamizh Kirukkan")
check(film_of('Kar Gayi Chull (From "Kapoor & Sons (Since 1921)")') == "Kapoor & Sons (Since 1921)")
check(clean_title('Hukum - Thalaivar Alappara - Tamil (From "Jailer")') == "Hukum - Thalaivar Alappara - Tamil")
check(clean_title("Gilli (Original Motion Picture Soundtrack)") == "Gilli")

# Tamil-script titles, dashes with a space on one side, singers named in the title
thoothu = "தூது வளை அரச்சு-Thoothu Valai -Mano S Janaki ,Love Duet Melody H D"
check(candidates(thoothu) == ["Thoothu Valai", "தூது வளை அரச்சு"])
check(song_name(thoothu) == "Thoothu Valai")
check(candidates("Gun-in Kadhal") == ["Gun-in Kadhal"])  # a dash inside a word is not a separator
# YouTube Music spells it "Thuthu Valaielle": accept only because the singers are named in the video title
check(is_match(thoothu, "Thoothu Valai", "Thuthu Valaielle", ["Mano, S Janaki"]))
check(not is_match(thoothu, "Thoothu Valai", "Thuthu Valaielle"))
check(is_match(thoothu, "தூது வளை அரச்சு", "Thuthu Valaielle", ["Mano, S Janaki"]))
check(not is_match(thoothu, "தூது வளை அரச்சு", "Thuthu Valaielle"))
check(not is_match(thoothu, "Thoothu Valai", "Thuthu Valaielle", ["Mano Raj"]))  # a different singer
# the singer rule loosens spelling only: other songs and other versions stay rejected
check(not is_match("Raavana Suthudae | Anirudh Ravichander", "Raavana Suthudae", "Raavana Mavandaa", ["Anirudh Ravichander"]))
check(not is_match("Rathinamo | Saurav Srisan", "Rathinamo", "Rathinamo (Female Version)", ["Saurav Srisan"]))



# resolve() against canned YouTube Music search results (real ones from Nov 2026
class FakeYT:
    def __init__(self, results):
        self.results = results

    def search(self, query, filter=None, limit=None):
        return self.results.get(query, [])


def song(title, *artists, video_id=None):
    return {"title": title, "artists": [{"name": a} for a in artists], "videoId": video_id or title}


def video(title, *artists):
    """A user upload, or with artists an official music video (YouTube lists the song's artists on it)."""
    if not artists:
        return {"title": title, "artists": [{"name": "Some Channel"}], "videoType": "MUSIC_VIDEO_TYPE_UGC"}
    return {"title": title, "artists": [{"name": a} for a in artists], "videoType": "MUSIC_VIDEO_TYPE_OMV"}


thuthu = song("Thuthu Valaielle", "Mano, S Janaki")
check(resolve(FakeYT({"Thoothu Valai": [thuthu]}), video(thoothu)) == thuthu)

# the second segment is the film: a song named after the film is not this song, even with its composer named
chinna = video("Chinna Chinna Seithi Solli 4K Video Song | Senthoorapandi | Vijay | Vijayakanth | Deva")
sendhoora = song("Sendhoora Pandikku", "Deva", "Vaali", "S. P. Balasubrahmanyam", "K. S. Chithra")
check(resolve(FakeYT({"Senthoorapandi": [sendhoora]}), chinna) is None)

# an exact spelling beats a looser one further up the results
enakke = video("Enakke Enakkaa Official Video | Full HD | Jeans | A.R.Rahman | Prashanth | Vairamuthu")
compilation = song('Ennake Ennaka (From "Jeans")', "Vairamuthu", "P. Unnikrishnan", "A.R. Rahman")
soundtrack = song("Enakke  Enakkaa", "Unnikrishnan")
check(resolve(FakeYT({"Enakke Enakkaa": [compilation, soundtrack]}), enakke) == soundtrack)

# all-Tamil titles: "தமிழ் பாடல்வரிகள்" (Tamil song lyrics) is not part of the name
check(candidates("ஏக் தோ தீன் தமிழ் பாடல்வரிகள்") == ["ஏக் தோ தீன்"])
check(candidates("அவ என்ன என்ன தமிழ் பாடல்வரிகள் | சூர்யா") == ["அவ என்ன என்ன", "சூர்யா"])
# nothing to compare letter by letter: the top result counts when the video lists its artist ("Yuvanshankar" too
ek_do_teen = song("Ek Do Teen", "Yuvanshankar Raja")
lyric_video = video("ஏக் தோ தீன் தமிழ் பாடல்வரிகள்", "Andrea Jeremiah", "Raghu Kunche", "Yuvan Shankar Raja")
check(resolve(FakeYT({"ஏக் தோ தீன் Andrea Jeremiah": [ek_do_teen]}), lyric_video) == ek_do_teen)
check(resolve(FakeYT({"ஏக் தோ தீன்": [ek_do_teen]}), video("ஏக் தோ தீன் தமிழ் பாடல்வரிகள்")) is None)  # unconfirmed
ava = video("அவ என்ன என்ன தமிழ் பாடல்வரிகள் | சூர்யா", "Prasanna", "Harris Jayaraj", "Karthik", "Thamarai")
ava_enna = song("Ava Enna", "Harris Jayaraj")
check(resolve(FakeYT({"அவ என்ன என்ன Prasanna": [ava_enna]}), ava) == ava_enna)
# "சூர்யா" is the actor: another Harris Jayaraj song found for it must not count
check(resolve(FakeYT({"சூர்யா Prasanna": [song("Anjala", "Harris Jayaraj")]}), ava) is None)

# a theme / background score is not the video's song (real case: Top Weekly, Nov 2026
check(not is_match("Gatta Kusthi 2", "Gatta Kusthi 2", "Gatta Kusthi 2 Theme", ["Sean Roldan"], ["Sean Roldan"]))
check(not is_match("Leo Video", "Leo", "Leo BGM", ["Anirudh Ravichander"], ["Anirudh Ravichander"]))

# B10: a song name that mixes scripts stays whole; split at a script change only at a dash or underscore
power = "#Video | वर्दी के Power | #Raushan Rohi , Srishti Bharti | Ft. Aparna Malik | Rangdari Son"
check("वर्दी के Power" in candidates(power))
check("Power" not in candidates(power))
check(candidates("தூது_வளை_Thoothu_Valai") == ["Thoothu_Valai", "தூது_வளை"])
check(candidates("ஏக் தோ தீன் Ek Do Teen") == ["ஏக் தோ தீன் Ek Do Teen"])

print(f"{check.runs - len(failed)} passed, {len(failed)} failed", "(lines " + ", ".join(map(str, failed)) + ")" if failed else "")
sys.exit(1 if failed else 0)
