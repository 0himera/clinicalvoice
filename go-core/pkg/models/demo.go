package models

// DemoDoctors is a small fixture for isolated unit tests. Runtime uses the workbook.
func DemoDoctors() []Doctor {
	return []Doctor{
		// Терапевтическое отделение (слот 20 мин)
		{ID: 1, FullName: "Васильева Татьяна Геннадьевна", Specialty: "терапевт", IsActive: true, Room: "210"},
		{ID: 2, FullName: "Кузнецов Андрей Павлович", Specialty: "терапевт", IsActive: true, Room: "212"},
		{ID: 3, FullName: "Орлова Марина Сергеевна", Specialty: "терапевт", IsActive: true, Room: "214"},
		{ID: 4, FullName: "Смирнова Елена Викторовна", Specialty: "терапевт", IsActive: true, Room: "216"},
		// Педиатрическое отделение (слот 20 мин)
		{ID: 5, FullName: "Белова Ольга Николаевна", Specialty: "педиатр", IsActive: true, Room: "101"},
		{ID: 6, FullName: "Громов Илья Александрович", Specialty: "педиатр", IsActive: true, Room: "102"},
		{ID: 7, FullName: "Попова Дарья Андреевна", Specialty: "педиатр", IsActive: true, Room: "103"},
		// ЛОР (слот 20 мин)
		{ID: 8, FullName: "Волков Артём Сергеевич", Specialty: "оториноларинголог", IsActive: true, Room: "301"},
		{ID: 9, FullName: "Медведева Алина Игоревна", Specialty: "оториноларинголог", IsActive: true, Room: "302"},
		{ID: 10, FullName: "Никитина Ирина Владимировна", Specialty: "оториноларинголог", IsActive: true, Room: "303"},
		// Офтальмологи (слот 20 мин)
		{ID: 11, FullName: "Егоров Максим Андреевич", Specialty: "офтальмолог", IsActive: true, Room: "305"},
		{ID: 12, FullName: "Морозова Светлана Петровна", Specialty: "офтальмолог", IsActive: true, Room: "306"},
		// Дерматовенеролог (слот 20 мин)
		{ID: 13, FullName: "Тихонова Екатерина Олеговна", Specialty: "дерматовенеролог", IsActive: true, Room: "401"},
		// Акушеры-гинекологи (слот 30 мин)
		{ID: 14, FullName: "Ковалёва Юлия Дмитриевна", Specialty: "акушер-гинеколог", IsActive: true, Room: "201"},
		{ID: 15, FullName: "Семёнова Виктория Алексеевна", Specialty: "акушер-гинеколог", IsActive: true, Room: "202"},
		// Хирург (слот 30 мин)
		{ID: 16, FullName: "Макаров Павел Сергеевич", Specialty: "хирург", IsActive: true, Room: "405"},
		// Травматологи (слот 30 мин)
		{ID: 17, FullName: "Зайцев Константин Ильич", Specialty: "травматолог-ортопед", IsActive: true, Room: "407"},
		{ID: 18, FullName: "Степанов Григорий Львович", Specialty: "травматолог-ортопед", IsActive: true, Room: "408"},
		// Неврологи (слот 30 мин, по направлению)
		{ID: 19, FullName: "Захаров Дмитрий Олегович", Specialty: "невролог", IsActive: true, Room: "501"},
		{ID: 20, FullName: "Фёдорова Наталья Юрьевна", Specialty: "невролог", IsActive: true, Room: "502"},
		// Кардиологи (слот 30 мин, по направлению)
		{ID: 21, FullName: "Киселёв Алексей Романович", Specialty: "кардиолог", IsActive: true, Room: "505"},
		{ID: 22, FullName: "Лебедев Сергей Игоревич", Specialty: "кардиолог", IsActive: true, Room: "506"},
		// Гастроэнтеролог (слот 30 мин, по направлению)
		{ID: 23, FullName: "Павлов Роман Евгеньевич", Specialty: "гастроэнтеролог", IsActive: true, Room: "508"},
		// Уролог (слот 30 мин, по направлению)
		{ID: 24, FullName: "Андреев Николай Васильевич", Specialty: "уролог", IsActive: true, Room: "510"},
		// Эндокринолог (слот 30 мин, по направлению)
		{ID: 25, FullName: "Соколова Анна Михайловна", Specialty: "эндокринолог", IsActive: true, Room: "512"},
	}

}
